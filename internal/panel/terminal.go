package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"starport-panel/internal/agent"
	"starport-panel/internal/panel/cluster"
)

// nodeTerminal 节点 Web 终端：WebSocket ↔ agent PTY 会话。
//
// 协议（浏览器侧 xterm.js 直接可接）：
//   - 二进制帧：终端字节流，双向。
//   - 文本帧：控制消息 JSON，{"type":"resize","cols":N,"rows":N}。
//   - 会话结束：服务端发文本帧 {"type":"exit","exitCode":N,"error":{...}} 后关闭。
//
// 查询参数：cols / rows 初始窗口（默认 120x30），shell（默认 `exec bash -l`，必须是 shell -c 可执行的脚本），
// tty=false 关闭伪终端（stdin/stdout 直通，适合非交互命令）。
func (s *Server) nodeTerminal(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.store.GetNode(r.Context(), nodeID); err != nil {
		writeErr(w, err)
		return
	}
	if !s.hub.Online(nodeID) {
		writeErr(w, &cluster.Error{Code: "NODE_OFFLINE", Message: "节点不在线", Status: 409})
		return
	}
	cols := queryInt(r, "cols", 120)
	rows := queryInt(r, "rows", 30)
	tty := r.URL.Query().Get("tty") != "false"
	script := r.URL.Query().Get("shell")
	if script == "" {
		script = "export TERM=xterm-256color; exec bash -l"
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Phase 2 无鉴权，面板只在内网/反代之后；同源策略交给反代
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn.SetReadLimit(1 << 20)

	sink := &wsSink{conn: conn, ctx: ctx, cancel: cancel}
	sessionID, err := s.hub.OpenSession(nodeID, script, tty, cols, rows, sink)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "节点不在线")
		return
	}
	defer s.hub.CloseSession(sessionID)

	// 读浏览器 → agent
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if err := s.hub.SessionStdin(sessionID, data); err != nil {
				return
			}
		case websocket.MessageText:
			var msg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				_ = s.hub.SessionResize(sessionID, msg.Cols, msg.Rows)
			}
		}
	}
}

// wsSink 把 agent 会话输出写回 WebSocket。
type wsSink struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *wsSink) OnData(data []byte) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	if err := s.conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		s.cancel()
	}
}

func (s *wsSink) OnExit(exitCode int, err *agent.Error) {
	msg, _ := json.Marshal(map[string]any{"type": "exit", "exitCode": exitCode, "error": err})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.conn.Write(ctx, websocket.MessageText, msg)
	_ = s.conn.Close(websocket.StatusNormalClosure, "session exit")
	s.cancel()
}

func queryInt(r *http.Request, key string, def int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || v <= 0 {
		return def
	}
	return v
}
