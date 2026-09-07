package installer

import (
	"context"
	"os"
	"strings"
)

// osPrepStep 系统准备：关 swap、加载内核模块（overlay/br_netfilter）、开启桥接转发 sysctl。
// 复用退役 server/internal/k8sinstall 的准备逻辑，改为原生 Go + 直连命令，不下发脚本。
func osPrepStep() Step {
	return Step{
		Name: "os_prep 系统准备",
		Skip: func(ctx context.Context) bool {
			return swapDisabled() &&
				sysctlIs("net.ipv4.ip_forward", "1") &&
				moduleLoaded("br_netfilter") &&
				moduleLoaded("overlay")
		},
		Run: func(ctx context.Context, log LogFunc) *Fail {
			if f := mustRun(ctx, log, "swapoff", "-a"); f != nil {
				return f
			}
			commentSwapInFstab(log)

			if f := writeFile("/etc/modules-load.d/k8s.conf", "overlay\nbr_netfilter\n", log); f != nil {
				return f
			}
			// 加载失败不致命（可能已内建），仅记录
			if code, _ := stream(ctx, log, "modprobe", "overlay"); code != 0 {
				emit(log, "warn: modprobe overlay 未成功（可能已内建）")
			}
			if code, _ := stream(ctx, log, "modprobe", "br_netfilter"); code != 0 {
				emit(log, "warn: modprobe br_netfilter 未成功（可能已内建）")
			}

			sysctl := "net.bridge.bridge-nf-call-iptables  = 1\n" +
				"net.bridge.bridge-nf-call-ip6tables = 1\n" +
				"net.ipv4.ip_forward                 = 1\n"
			if f := writeFile("/etc/sysctl.d/99-kubernetes-cri.conf", sysctl, log); f != nil {
				return f
			}
			if f := mustRun(ctx, log, "sysctl", "--system"); f != nil {
				return f
			}
			return nil
		},
	}
}

// commentSwapInFstab 注释掉 /etc/fstab 中的 swap 行，避免重启后 swap 复活（best-effort）。
func commentSwapInFstab(log LogFunc) {
	const path = "/etc/fstab"
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	changed := false
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		fields := strings.Fields(t)
		if len(fields) >= 3 && fields[2] == "swap" {
			lines[i] = "# " + ln
			changed = true
		}
	}
	if changed {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err == nil {
			emit(log, "已注释 /etc/fstab 中的 swap 挂载")
		}
	}
}
