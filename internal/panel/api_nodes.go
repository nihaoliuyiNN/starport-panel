package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/cluster"
	"starport-panel/internal/panel/store"
)

// upgradeRequest 升级 agent：从 binUrl 下载新二进制，校验 sha256（可选）后替换并重启 systemd 服务。
type upgradeRequest struct {
	BinURL  string   `json:"binUrl"`
	SHA256  string   `json:"sha256,omitempty"`
	NodeIDs []uint64 `json:"nodeIds,omitempty"` // 批量接口用；空 = 全部在线节点
}

var sha256Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func (r upgradeRequest) validate() error {
	u, err := url.Parse(strings.TrimSpace(r.BinURL))
	if r.BinURL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return &cluster.Error{Code: "INVALID_ARGUMENT", Message: "binUrl 须为 http(s) 地址", Status: 400}
	}
	if r.SHA256 != "" && !sha256Re.MatchString(r.SHA256) {
		return &cluster.Error{Code: "INVALID_ARGUMENT", Message: "sha256 须为 64 位十六进制", Status: 400}
	}
	return nil
}

// upgradeScript 在节点上执行：下载 → 校验 → 试运行 --version → 覆盖正在运行的二进制（按 /proc 反查路径）→
// 延迟 3 秒重启服务（脚本先正常返回，任务才有终态；重启后 agent 以新版本重连）。
// 用 systemd-run 定时器触发重启；没有 systemd-run 时退化为 setsid 后台 sleep。
const upgradeScript = `set -euo pipefail
URL=%s
SUM=%s
SVC="${STARPORT_AGENT_SERVICE:-starport-agent}"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
echo "[upgrade] 下载 $URL"
if command -v curl >/dev/null 2>&1; then curl -fsSL "$URL" -o "$tmp"
elif command -v wget >/dev/null 2>&1; then wget -qO "$tmp" "$URL"
else echo "缺少 curl/wget" >&2; exit 1; fi
if [ -n "$SUM" ]; then
  echo "$SUM  $tmp" | sha256sum -c - >/dev/null || { echo "[upgrade] sha256 不匹配" >&2; exit 1; }
  echo "[upgrade] sha256 校验通过"
fi
chmod 0755 "$tmp"
NEWVER="$("$tmp" --version 2>&1 | tail -n1 || true)"
echo "[upgrade] 新版本: ${NEWVER:-未知}"
BIN="$(readlink -f /proc/$PPID/exe 2>/dev/null || true)"
case "$BIN" in
  */starport-agent*) ;;
  *) BIN=/usr/local/bin/starport-agent ;;
esac
echo "[upgrade] 替换 $BIN"
install -m 0755 "$tmp" "$BIN"
if command -v systemd-run >/dev/null 2>&1; then
  systemd-run --quiet --on-active=3 --unit "starport-agent-restart-$$" systemctl restart "$SVC"
else
  setsid nohup sh -c "sleep 3; systemctl restart $SVC" >/dev/null 2>&1 < /dev/null &
fi
echo "[upgrade] 已安排 3 秒后重启 $SVC，agent 将以新版本重连"
`

// deleteNode DELETE /api/v1/nodes/{id}：删除节点记录（退役机器）。仍属于某集群则拒绝——先从集群移除。
// 在线的连接会被踢掉；其令牌随记录一起失效，agent 若还在跑会重注册成新节点（需先停掉 agent 服务）。
func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	ms, err := s.store.NodeMemberships(r.Context(), nodeID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(ms) > 0 {
		writeErr(w, &cluster.Error{Code: "NODE_IN_CLUSTER", Message: fmt.Sprintf("节点仍属于集群 %d，请先移除", ms[0].ClusterID), Status: 409})
		return
	}
	if err := s.store.DeleteNode(r.Context(), nodeID); err != nil {
		writeErr(w, err)
		return
	}
	s.hub.Kick(nodeID)
	w.WriteHeader(http.StatusNoContent)
}

// upgradeNode POST /api/v1/nodes/{id}/upgrade → {taskId}
func (s *Server) upgradeNode(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req upgradeRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.store.GetNode(r.Context(), nodeID); err != nil {
		writeErr(w, err)
		return
	}
	taskID, err := s.startUpgrade(nodeID, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]uint64{"taskId": taskID})
}

// upgradeNodes POST /api/v1/nodes/upgrade → {tasks:{nodeId:taskId}, skipped:[nodeId]}；nodeIds 空 = 全部在线节点。
func (s *Server) upgradeNodes(w http.ResponseWriter, r *http.Request) {
	var req upgradeRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil {
		writeErr(w, err)
		return
	}
	ids := req.NodeIDs
	if len(ids) == 0 {
		nodes, err := s.store.ListNodes(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		for _, n := range nodes {
			if n.Online {
				ids = append(ids, n.ID)
			}
		}
	}
	tasks := map[uint64]uint64{}
	skipped := []uint64{}
	for _, id := range ids {
		taskID, err := s.startUpgrade(id, req)
		if err != nil {
			skipped = append(skipped, id)
			continue
		}
		tasks[id] = taskID
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"tasks": tasks, "skipped": skipped})
}

func (s *Server) startUpgrade(nodeID uint64, req upgradeRequest) (uint64, error) {
	if !s.hub.Online(nodeID) {
		return 0, &cluster.Error{Code: "NODE_OFFLINE", Message: fmt.Sprintf("节点 %d 不在线", nodeID), Status: 409}
	}
	script := fmt.Sprintf(upgradeScript, shq(strings.TrimSpace(req.BinURL)), shq(strings.ToLower(req.SHA256)))
	return s.tasks.Start(store.TaskUpgrade, nodeID, 0, func(ctx context.Context, logf func(string)) (agenthub.Result, error) {
		return s.hub.Exec(ctx, nodeID, script, 10*time.Minute, logf)
	}, nil)
}

// shq 单引号包裹的 shell 字面量（内部的 ' 转为 '\”），杜绝变量展开与注入。
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
