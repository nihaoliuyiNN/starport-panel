<#
.SYNOPSIS
  构建 starport-agent 并发布到 Gitee Release（建 Release + 传附件 + 校验直链）。

.DESCRIPTION
  代替「本地编译 → 打开网页 → 手动拖附件」这一串手工活。附件名保持 starport-agent /
  install-starport-agent.sh 不变，所以 docs/build.md 里那些 releases/download/<tag>/<file>
  直链口径不用改，一键更新弹窗里填的地址只需换 tag。

  令牌不走命令行参数（会进 shell 历史），从文件读。Gitee 私人令牌在
  「设置 → 安全设置 → 私人令牌」生成，勾 projects 权限即可。

.EXAMPLE
  .\publish-agent-release.ps1 -Version v1.0.9
.EXAMPLE
  # 二进制已经编好了，只补传附件
  .\publish-agent-release.ps1 -Version v1.0.9 -SkipBuild
#>
[CmdletBinding()]
param(
    # 版本号，同时注入二进制的 main.version 并作为 Release 的 tag
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$TokenFile = 'E:\Develop\.gitee-token',
    [string]$Owner = 'nihaoliuyi',
    [string]$Repo = 'starport-agent',
    [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64',
    # Release 挂在哪个提交上（tag 已存在时该参数被忽略）
    [string]$Commitish = 'master',
    [switch]$SkipBuild,
    # 附件同名时先删旧的再传；不加则遇到同名直接报错，避免一个 Release 挂两份同名文件
    [switch]$Replace
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Add-Type -AssemblyName System.Net.Http

$goRoot = Split-Path -Parent $PSScriptRoot
$distDir = Join-Path $goRoot 'dist'
$apiBase = "https://gitee.com/api/v5/repos/$Owner/$Repo"

function Read-Token {
    if (-not (Test-Path $TokenFile)) {
        throw "令牌文件不存在：$TokenFile（Gitee 设置 → 安全设置 → 私人令牌，勾 projects 权限）"
    }
    $t = (Get-Content $TokenFile -Raw).Trim()
    if (-not $t) { throw "令牌文件为空：$TokenFile" }
    return $t
}

function Build-Agent {
    Write-Host "[1/5] 交叉编译 linux/$Arch，版本 $Version"
    Push-Location $goRoot
    try {
        $env:GOOS = 'linux'; $env:GOARCH = $Arch; $env:CGO_ENABLED = '0'
        & go build -ldflags "-s -w -X main.version=$Version" -o dist/starport-agent ./cmd/starport-agent
        if ($LASTEXITCODE -ne 0) { throw "go build 失败（exit $LASTEXITCODE）" }
    }
    finally {
        Pop-Location
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }
}

# 安装脚本必须是 LF：Gitee 上传 CRLF 版本后，节点 curl | bash 会报
# 「$'\r': command not found」——docs/build.md 里记过这个坑，这里直接转掉根治。
function Copy-InstallScriptAsLf {
    $src = Join-Path $PSScriptRoot 'install-starport-agent.sh'
    $dst = Join-Path $distDir 'install-starport-agent.sh'
    $text = [IO.File]::ReadAllText($src) -replace "`r`n", "`n"
    [IO.File]::WriteAllText($dst, $text, [Text.UTF8Encoding]::new($false))
    return $dst
}

function Get-ReleaseByTag([string]$token) {
    # tag 不存在时 Gitee 不给 404，而是 200 + 正文 "null"（PowerShell 会把它当普通字符串
    # 收下，直接 if 判真会误判成「已存在」），所以一律以能不能取到数字 id 为准。
    try {
        $r = Invoke-RestMethod -Uri "$apiBase/releases/tags/$Version`?access_token=$token" -Method Get -TimeoutSec 60
    }
    catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 404) { return $null }
        throw
    }
    if ($r -isnot [psobject] -or -not $r.PSObject.Properties['id']) { return $null }
    $id = $r.id -as [long]
    if (-not $id) { return $null }
    return $r
}

function New-Release([string]$token) {
    $body = @{
        access_token     = $token
        tag_name         = $Version
        name             = $Version
        body             = "starport-agent $Version (linux/$Arch)"
        target_commitish = $Commitish
        prerelease       = 'false'
    }
    return Invoke-RestMethod -Uri "$apiBase/releases" -Method Post -Body $body -TimeoutSec 120
}

function Remove-SameNameAttachments([string]$token, [long]$releaseId, [string[]]$names) {
    $list = Invoke-RestMethod -Uri "$apiBase/releases/$releaseId/attach_files`?access_token=$token" -Method Get -TimeoutSec 60
    foreach ($a in @($list)) {
        if ($names -notcontains $a.name) { continue }
        if (-not $Replace) {
            throw "Release $Version 已挂同名附件 $($a.name)；确认要覆盖请加 -Replace"
        }
        Write-Host "      删除旧附件 $($a.name) (id=$($a.id))"
        Invoke-RestMethod -Uri "$apiBase/releases/$releaseId/attach_files/$($a.id)`?access_token=$token" -Method Delete -TimeoutSec 60 | Out-Null
    }
}

function Send-Attachment([string]$token, [long]$releaseId, [string]$path) {
    $name = Split-Path -Leaf $path
    $client = [Net.Http.HttpClient]::new()
    $client.Timeout = [TimeSpan]::FromMinutes(10)
    $form = [Net.Http.MultipartFormDataContent]::new()
    $stream = [IO.File]::OpenRead($path)
    try {
        $file = [Net.Http.StreamContent]::new($stream)
        $file.Headers.ContentType = [Net.Http.Headers.MediaTypeHeaderValue]::new('application/octet-stream')
        $form.Add($file, 'file', $name)
        # 令牌必须走 query：swagger 把 access_token 标成 formData，但实际混在 multipart
        # 里传会被判 40001「登录失效」，只有 query 参数认。
        $url = "$apiBase/releases/$releaseId/attach_files`?access_token=$token"
        $resp = $client.PostAsync($url, $form).GetAwaiter().GetResult()
        $text = $resp.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        if (-not $resp.IsSuccessStatusCode) {
            throw "上传 $name 失败：HTTP $([int]$resp.StatusCode) $text"
        }
        Write-Host "      $name 上传成功"
    }
    finally {
        $stream.Dispose(); $form.Dispose(); $client.Dispose()
    }
}

# 校验的是公开直链而不是 API 返回值：一键更新和安装脚本走的就是这个 URL，
# 只有它下回来的字节和本地一致才算真发出去了。
function Test-PublishedFile([string]$path) {
    $name = Split-Path -Leaf $path
    $url = "https://gitee.com/$Owner/$Repo/releases/download/$Version/$name"
    $tmp = Join-Path $env:TEMP "publish-verify-$name"
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $tmp -TimeoutSec 600
    $remote = (Get-FileHash $tmp -Algorithm SHA256).Hash.ToLower()
    $local = (Get-FileHash $path -Algorithm SHA256).Hash.ToLower()
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
    if ($remote -ne $local) { throw "$name 直链校验不一致：远端 $remote 本地 $local" }
    Write-Host "      $name sha256 一致 $local"
    return $url
}

$token = Read-Token

if ($SkipBuild) {
    Write-Host "[1/5] 跳过编译，用现有 dist/starport-agent"
    if (-not (Test-Path (Join-Path $distDir 'starport-agent'))) { throw '缺少 dist/starport-agent，去掉 -SkipBuild 重跑' }
}
else {
    Build-Agent
}

Write-Host '[2/5] 准备附件'
$assets = @((Join-Path $distDir 'starport-agent'), (Copy-InstallScriptAsLf))
foreach ($a in $assets) {
    $f = Get-Item $a
    Write-Host "      $($f.Name) $($f.Length) 字节 sha256=$((Get-FileHash $a -Algorithm SHA256).Hash.ToLower())"
}

Write-Host "[3/5] 定位 Release $Version"
$release = Get-ReleaseByTag $token
if ($release) {
    Write-Host "      已存在，复用 release id=$($release.id)"
    Remove-SameNameAttachments $token $release.id ($assets | ForEach-Object { Split-Path -Leaf $_ })
}
else {
    $release = New-Release $token
    Write-Host "      新建成功 id=$($release.id) tag=$Version"
}

Write-Host '[4/5] 上传附件'
foreach ($a in $assets) { Send-Attachment $token $release.id $a }

Write-Host '[5/5] 校验公开直链'
$urls = foreach ($a in $assets) { Test-PublishedFile $a }

Write-Host ''
Write-Host "发布完成 $Version"
$urls | ForEach-Object { Write-Host "  $_" }
