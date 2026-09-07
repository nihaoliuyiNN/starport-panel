<#
.SYNOPSIS
  构建面板 + agent 并发布到 GitHub Release（建 Release、传附件、校验直链）。

.DESCRIPTION
  产物与附件名（安装脚本按这些名字去 releases/latest/download/ 拿）：
    starport-panel-linux-amd64        面板（内嵌 Web UI）
    starport-agent-linux-amd64        节点代理
    starport-agent-linux-arm64
    install-starport-panel.sh         一行安装脚本（转成 LF 再传）
    install-starport-agent.sh
    SHA256SUMS

  令牌从文件读，不走命令行参数。GitHub「Settings → Developer settings → Personal access tokens」
  生成，classic 勾 repo；fine-grained 给本仓库 Contents: Read and write。

.EXAMPLE
  .\publish-release.ps1 -Version v0.1.0
.EXAMPLE
  # 已经编好，只补传附件并覆盖同名
  .\publish-release.ps1 -Version v0.1.0 -SkipBuild -Replace
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$TokenFile = 'E:\Develop\.github-token',
    [string]$Owner = 'nihaoliuyiNN',
    [string]$Repo = 'starport-panel',
    [string]$Commitish = 'main',
    [switch]$SkipBuild,
    [switch]$SkipUI,
    [switch]$Replace,
    [switch]$Prerelease
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$root = Split-Path -Parent $PSScriptRoot
$distDir = Join-Path $root 'dist'
$api = "https://api.github.com/repos/$Owner/$Repo"
$uploads = "https://uploads.github.com/repos/$Owner/$Repo"

function Read-Token {
    if (-not (Test-Path $TokenFile)) { throw "令牌文件不存在：$TokenFile" }
    $t = (Get-Content $TokenFile -Raw).Trim()
    if (-not $t) { throw "令牌文件为空：$TokenFile" }
    return $t
}

function Headers([string]$token) {
    return @{
        Authorization          = "Bearer $token"
        Accept                 = 'application/vnd.github+json'
        'X-GitHub-Api-Version' = '2022-11-28'
        'User-Agent'           = 'starport-publish'
    }
}

function Invoke-Go([string[]]$goArgs, [string]$goos, [string]$goarch) {
    $env:GOOS = $goos; $env:GOARCH = $goarch; $env:CGO_ENABLED = '0'
    try {
        & go @goArgs
        if ($LASTEXITCODE -ne 0) { throw "go $($goArgs[0]) 失败（exit $LASTEXITCODE）" }
    }
    finally {
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }
}

function Build-All {
    Push-Location $root
    try {
        if (-not $SkipUI) {
            Write-Host '[1/5] 构建 Web UI'
            Push-Location (Join-Path $root 'web')
            try {
                & pnpm install --frozen-lockfile
                if ($LASTEXITCODE -ne 0) { throw 'pnpm install 失败' }
                & pnpm build
                if ($LASTEXITCODE -ne 0) { throw 'pnpm build 失败' }
            }
            finally { Pop-Location }
        }
        else {
            Write-Host '[1/5] 跳过 UI 构建，用现有 internal/panel/ui/dist'
        }
        $ld = "-s -w -X main.version=$Version"
        Write-Host "      编译 starport-panel linux/amd64 $Version"
        Invoke-Go @('build', '-ldflags', $ld, '-o', 'dist/starport-panel-linux-amd64', './cmd/starport-panel') 'linux' 'amd64'
        foreach ($arch in 'amd64', 'arm64') {
            Write-Host "      编译 starport-agent linux/$arch $Version"
            Invoke-Go @('build', '-ldflags', $ld, '-o', "dist/starport-agent-linux-$arch", './cmd/starport-agent') 'linux' $arch
        }
    }
    finally { Pop-Location }
}

# 安装脚本必须是 LF，否则 curl | bash 报 $'\r': command not found
function Copy-ScriptAsLf([string]$name) {
    $src = Join-Path $PSScriptRoot $name
    $dst = Join-Path $distDir $name
    $text = [IO.File]::ReadAllText($src) -replace "`r`n", "`n"
    [IO.File]::WriteAllText($dst, $text, [Text.UTF8Encoding]::new($false))
    return $dst
}

function Write-Sums([string[]]$files) {
    $dst = Join-Path $distDir 'SHA256SUMS'
    $lines = foreach ($f in $files) { "$((Get-FileHash $f -Algorithm SHA256).Hash.ToLower())  $(Split-Path -Leaf $f)" }
    [IO.File]::WriteAllText($dst, ($lines -join "`n") + "`n", [Text.UTF8Encoding]::new($false))
    return $dst
}

function Get-ReleaseByTag([string]$token) {
    try {
        return Invoke-RestMethod -Uri "$api/releases/tags/$Version" -Headers (Headers $token) -TimeoutSec 60
    }
    catch {
        if ($_.Exception.Response.StatusCode.value__ -eq 404) { return $null }
        throw
    }
}

function New-Release([string]$token) {
    $body = @{
        tag_name         = $Version
        name             = $Version
        body             = "starport-panel $Version`n`n一行安装见 README。附件 sha256 见 SHA256SUMS。"
        target_commitish = $Commitish
        prerelease       = [bool]$Prerelease
        draft            = $false
    } | ConvertTo-Json
    return Invoke-RestMethod -Uri "$api/releases" -Method Post -Headers (Headers $token) -Body $body -ContentType 'application/json' -TimeoutSec 120
}

function Remove-SameNameAssets([string]$token, [long]$releaseId, [string[]]$names) {
    $list = Invoke-RestMethod -Uri "$api/releases/$releaseId/assets?per_page=100" -Headers (Headers $token) -TimeoutSec 60
    foreach ($a in @($list)) {
        if ($names -notcontains $a.name) { continue }
        if (-not $Replace) { throw "Release $Version 已有同名附件 $($a.name)；要覆盖请加 -Replace" }
        Write-Host "      删除旧附件 $($a.name)"
        Invoke-RestMethod -Uri "$api/releases/assets/$($a.id)" -Method Delete -Headers (Headers $token) -TimeoutSec 60 | Out-Null
    }
}

function Send-Asset([string]$token, [long]$releaseId, [string]$path) {
    $name = Split-Path -Leaf $path
    $h = Headers $token
    $h['Content-Type'] = 'application/octet-stream'
    Invoke-RestMethod -Uri "$uploads/releases/$releaseId/assets?name=$([Uri]::EscapeDataString($name))" -Method Post -Headers $h -InFile $path -TimeoutSec 900 | Out-Null
    Write-Host "      $name 上传成功"
}

# 校验公开直链（安装脚本走的就是它），字节一致才算发出去了
function Test-PublishedFile([string]$path) {
    $name = Split-Path -Leaf $path
    $url = "https://github.com/$Owner/$Repo/releases/download/$Version/$name"
    $tmp = Join-Path $env:TEMP "publish-verify-$name"
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $tmp -TimeoutSec 600
    $remote = (Get-FileHash $tmp -Algorithm SHA256).Hash.ToLower()
    $local = (Get-FileHash $path -Algorithm SHA256).Hash.ToLower()
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
    if ($remote -ne $local) { throw "$name 直链校验不一致" }
    Write-Host "      $name ok"
    return $url
}

$token = Read-Token
New-Item -ItemType Directory -Force $distDir | Out-Null

if ($SkipBuild) {
    Write-Host '[1/5] 跳过编译，用现有 dist/'
}
else {
    Build-All
}

Write-Host '[2/5] 准备附件'
$bins = @('starport-panel-linux-amd64', 'starport-agent-linux-amd64', 'starport-agent-linux-arm64') | ForEach-Object {
    $p = Join-Path $distDir $_
    if (-not (Test-Path $p)) { throw "缺少 $p" }
    $p
}
$scripts = @('install-starport-panel.sh', 'install-starport-agent.sh') | ForEach-Object { Copy-ScriptAsLf $_ }
$assets = @($bins + $scripts)
$assets += Write-Sums $assets
foreach ($a in $assets) {
    $f = Get-Item $a
    Write-Host ("      {0,-32} {1,12:N0} B" -f $f.Name, $f.Length)
}

Write-Host "[3/5] 定位 Release $Version"
$release = Get-ReleaseByTag $token
if ($release) {
    Write-Host "      已存在 id=$($release.id)，复用"
    Remove-SameNameAssets $token $release.id ($assets | ForEach-Object { Split-Path -Leaf $_ })
}
else {
    $release = New-Release $token
    Write-Host "      新建 id=$($release.id) tag=$Version"
}

Write-Host '[4/5] 上传附件'
foreach ($a in $assets) { Send-Asset $token $release.id $a }

Write-Host '[5/5] 校验公开直链'
$urls = foreach ($a in $assets) { Test-PublishedFile $a }

Write-Host ''
Write-Host "发布完成 $Version  $($release.html_url)"
$urls | ForEach-Object { Write-Host "  $_" }
