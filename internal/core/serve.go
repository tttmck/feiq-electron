package core

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

	"feiq-tui/internal/ipmsg"
)

// RPC 请求/响应与事件通过 stdin/stdout 的逐行 JSON 交换：
//   请求: {"id":1,"method":"send_msg","params":{...}}
//   响应: {"id":1,"result":{...}} 或 {"id":1,"error":"..."}
//   事件: {"event":"msg","data":{...}}
// 日志走 stderr，不污染 stdout 协议流。

type rpcRequest struct {
	ID     int            `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

type rpcResponse struct {
	ID     int    `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type rpcEvent struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// SelfInfo 是本机身份的对外描述。
type SelfInfo struct {
	Nickname    string `json:"nickname"`
	Group       string `json:"group"`
	Stealth     bool   `json:"stealth"`
	Port        int    `json:"port"`
	DownloadDir string `json:"download_dir"`
}

// GetSelf 返回本机身份。
func (c *Core) GetSelf() SelfInfo {
	return SelfInfo{
		Nickname:    c.cfg.Nickname,
		Group:       c.cfg.Group,
		Stealth:     c.cfg.Stealth,
		Port:        c.cfg.Port,
		DownloadDir: c.cfg.DownloadDir,
	}
}

// eventName 把事件结构映射为短名。
func eventName(ev any) string {
	switch ev.(type) {
	case PalEvent:
		return "pal"
	case MsgInEvent:
		return "msg"
	case AckEvent:
		return "ack"
	case FileOfferEvent:
		return "offer"
	case TransEvent:
		return "trans"
	case NoticeEvent:
		return "notice"
	default:
		return fmt.Sprintf("%T", ev)
	}
}

// handleRPC 执行一条 RPC 请求。
func (c *Core) handleRPC(req *rpcRequest) (any, error) {
	strParam := func(k string) string {
		if v, ok := req.Params[k].(string); ok {
			return v
		}
		return ""
	}
	boolParam := func(k string) bool {
		if v, ok := req.Params[k].(bool); ok {
			return v
		}
		return false
	}
	strListParam := func(k string) []string {
		var out []string
		if arr, ok := req.Params[k].([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}

	switch req.Method {
	case "get_state":
		return map[string]any{
			"self": c.GetSelf(),
			"pals": c.Pals().All(),
		}, nil
	case "refresh":
		c.BroadcastEntry()
		return map[string]any{"ok": true}, nil
	case "send_msg":
		ips := strListParam("ips")
		if len(ips) == 0 {
			return nil, fmt.Errorf("缺少 ips")
		}
		c.SendMessage(ips, strParam("text"))
		return map[string]any{"ok": true}, nil
	case "broadcast":
		c.BroadcastText(strParam("text"))
		return map[string]any{"ok": true}, nil
	case "send_files":
		ips := strListParam("ips")
		paths := strListParam("paths")
		if len(ips) == 0 || len(paths) == 0 {
			return nil, fmt.Errorf("缺少 ips 或 paths")
		}
		c.SendFiles(ips, paths, strParam("note"))
		return map[string]any{"ok": true}, nil
	case "accept_files":
		pn, err := numParam(req.Params["packet_no"])
		if err != nil {
			return nil, fmt.Errorf("packet_no 无效")
		}
		c.AcceptFiles(uint32(pn), strParam("dir"))
		return map[string]any{"ok": true}, nil
	case "reject_files":
		pn, err := numParam(req.Params["packet_no"])
		if err != nil {
			return nil, fmt.Errorf("packet_no 无效")
		}
		ip := strParam("ip")
		c.RejectFiles(ip, uint32(pn))
		return map[string]any{"ok": true}, nil
	case "set_absence":
		c.SetAbsence(boolParam("on"), "离开")
		return map[string]any{"ok": true}, nil
	case "load_history":
		n := 50
		if v, err := numParam(req.Params["lines"]); err == nil && v > 0 && v <= 500 {
			n = int(v)
		}
		return map[string]any{"lines": c.HistoryLoad(strParam("ip"), n)}, nil
	default:
		return nil, fmt.Errorf("未知方法 %s", req.Method)
	}
}

func numParam(v any) (int64, error) {
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0, fmt.Errorf("非数值")
		}
		n = f
	default:
		return 0, fmt.Errorf("非数值")
	}
	if n != math.Trunc(n) || n < 0 || n > math.MaxUint32 {
		return 0, fmt.Errorf("数值超出范围")
	}
	return int64(n), nil
}

// RunServe 以服务模式运行核心：stdin 读 RPC，stdout 写响应与事件。
// stdin 关闭（父进程退出）即整体退出。
func RunServe(cfg Config) error {
	return runServe(os.Stdin, os.Stdout, cfg)
}

// runServe 是 RunServe 的可测内核：从 r 读请求，向 w 写响应与事件。
func runServe(r io.Reader, w io.Writer, cfg Config) error {
	c := New(cfg)
	if err := c.Start(); err != nil {
		return err
	}
	defer c.Shutdown()
	c.BroadcastEntry()

	enc := json.NewEncoder(w)
	// json.Encoder 不承诺并发安全，虽然当前 os.Stdout 的写在运行时层面
	// 串行，仍加锁保证协议流永不交错（换成带缓冲 writer 也不怕）。
	var encMu sync.Mutex
	writeMsg := func(v any) error {
		encMu.Lock()
		defer encMu.Unlock()
		return enc.Encode(v)
	}

	// 事件泵：core 事件 → notification
	go func() {
		for ev := range c.Events() {
			_ = writeMsg(rpcEvent{Event: eventName(ev), Data: ev})
		}
	}()

	dec := json.NewDecoder(r)
	for {
		var req rpcRequest
		if err := dec.Decode(&req); err != nil {
			return nil // 输入结束/损坏：跟随父进程退出
		}
		resp := rpcResponse{ID: req.ID}
		result, rerr := c.handleRPC(&req)
		if rerr != nil {
			resp.Error = rerr.Error()
		} else {
			resp.Result = result
		}
		if err := writeMsg(resp); err != nil {
			return err
		}
	}
}

// 编译期断言：serve 用到的 ipmsg 符号仍在（防止 import 清理误删）。
var _ = ipmsg.DefaultPort
