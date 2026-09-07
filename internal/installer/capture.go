package installer

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var reHex64 = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

// capture 首 master 装机后采集 join 凭据与 kubeconfig，供控制面编排其余节点加入。
func capture(ctx context.Context, spec *InstallSpec, log LogFunc) (*InstallResult, *Fail) {
	setupRootKubeconfig(log)

	joinCmd, err := output(ctx, "kubeadm", "token", "create", "--print-join-command")
	if err != nil {
		return nil, failf(ErrStep, "生成 join 命令失败: %v", err)
	}
	joinCmd = strings.TrimSpace(lastNonEmptyLine(joinCmd))
	token, caHash, endpoint := parseJoinCommand(joinCmd)

	// 重新上传证书，得到 join-master 用的 certificate-key（2h 有效）
	certOut, err := output(ctx, "kubeadm", "init", "phase", "upload-certs", "--upload-certs")
	if err != nil {
		return nil, failf(ErrStep, "上传证书失败: %v", err)
	}
	certKey := reHex64.FindString(certOut)

	kubeconfig, rerr := os.ReadFile(adminConf)
	if rerr != nil {
		return nil, failf(ErrStep, "读取 admin.conf 失败: %v", rerr)
	}

	if endpoint == "" {
		endpoint = spec.ControlPlaneEndpoint
	}
	emit(log, "首 master 装机完成，已采集 join 凭据与 kubeconfig")
	return &InstallResult{
		JoinCommand:          joinCmd,
		Token:                token,
		CACertHash:           caHash,
		CertificateKey:       certKey,
		ControlPlaneEndpoint: endpoint,
		KubeconfigB64:        base64.StdEncoding.EncodeToString(kubeconfig),
	}, nil
}

// setupRootKubeconfig 复制 admin.conf 到 root 家目录，方便节点上直接 kubectl（best-effort）。
func setupRootKubeconfig(log LogFunc) {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	dst := filepath.Join(home, ".kube", "config")
	b, err := os.ReadFile(adminConf)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(dst, b, 0o600); err == nil {
		emit(log, "已写入 "+dst)
	}
}

// parseJoinCommand 从 `kubeadm join HOST:PORT --token T --discovery-token-ca-cert-hash H` 解析要素。
func parseJoinCommand(cmd string) (token, caHash, endpoint string) {
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "join":
			if i+1 < len(fields) {
				endpoint = fields[i+1]
			}
		case "--token":
			if i+1 < len(fields) {
				token = fields[i+1]
			}
		case "--discovery-token-ca-cert-hash":
			if i+1 < len(fields) {
				caHash = fields[i+1]
			}
		}
	}
	return
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
