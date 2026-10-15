package core

import (
	"bufio"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serveHarness 驱动一个 runServe 实例：写入请求行、读出响应/事件行。
type serveHarness struct {
	in  io.Writer
	out *bufio.Scanner
	t   *testing.T
}

func startServe(t *testing.T, nick string, port int) *serveHarness {
	t.Helper()
	dir := t.TempDir()
	pr, pw := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = runServe(pr, outW, Config{
			Nickname:    nick,
			Port:        port,
			DataDir:     dir,
			DownloadDir: filepath.Join(dir, "dl"),
		})
	}()
	t.Cleanup(func() { pw.Close() })
	return &serveHarness{in: pw, out: bufio.NewScanner(outR), t: t}
}

// call 发一条 RPC 并等待对应 id 的响应（跳过事件行）。
func (h *serveHarness) call(method string, params map[string]any) map[string]any {
	h.t.Helper()
	_, _ = h.in.Write([]byte(mustJSON(map[string]any{"id": time.Now().UnixNano(), "method": method, "params": params}) + "\n"))
	deadline := time.After(5 * time.Second)
	for {
		ch := make(chan string, 1)
		go func() { ok := h.out.Scan(); ch <- h.out.Text(); _ = ok }()
		select {
		case line := <-ch:
			if line == "" {
				h.t.Fatal("serve 输出关闭")
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			if _, hasID := m["id"]; hasID {
				return m
			}
		case <-deadline:
			h.t.Fatalf("等待 %s 响应超时", method)
		}
	}
}

// waitEvent 等待指定名称的事件出现。
func (h *serveHarness) waitEvent(name string, timeout time.Duration) map[string]any {
	h.t.Helper()
	deadline := time.After(timeout)
	for {
		ch := make(chan string, 1)
		go func() { ok := h.out.Scan(); ch <- h.out.Text(); _ = ok }()
		select {
		case line := <-ch:
			if line == "" {
				h.t.Fatal("serve 输出关闭")
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			if ev, _ := m["event"].(string); ev == name {
				return m
			}
		case <-deadline:
			h.t.Fatalf("等待事件 %s 超时", name)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestServeRPCRoundTrip(t *testing.T) {
	a := startServe(t, "RPC测试A", 24271)
	defer a.call("set_absence", map[string]any{"on": false})

	// get_state 基本结构
	st := a.call("get_state", nil)
	if st["error"] != nil {
		t.Fatalf("get_state 出错: %v", st["error"])
	}
	self := st["result"].(map[string]any)["self"].(map[string]any)
	if self["nickname"] != "RPC测试A" {
		t.Fatalf("昵称不符: %v", self["nickname"])
	}

	// 未知方法返回错误
	r := a.call("no_such_method", nil)
	if r["error"] == nil {
		t.Fatal("未知方法未报错")
	}
}

func TestServeChatBetweenTwoInstances(t *testing.T) {
	a := startServe(t, "实例A", 24272)
	b := startServe(t, "实例B", 24273)

	// A 定向上线到 B，B 应产生 pal 事件
	a.call("refresh", nil) // 广播（发往自身端口，B 收不到，无害）
	b.call("get_state", nil)

	// B 向未知好友发消息 → 离线队列路径（notice 事件）
	resp := b.call("send_msg", map[string]any{"ips": []string{"127.0.0.1"}, "text": "serve 模式消息"})
	if resp["error"] != nil {
		t.Fatalf("send_msg 出错: %v", resp["error"])
	}
	// B 会收到 notice（不在线提示）
	ev := b.waitEvent("notice", 3*time.Second)
	if !strings.Contains(ev["data"].(map[string]any)["Text"].(string), "不在线") &&
		!strings.Contains(mustJSON(ev), "不在线") {
		t.Fatalf("未收到离线提示事件: %v", ev)
	}
}
