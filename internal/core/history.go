package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// History 负责聊天记录的本地持久化，按好友 IP 分文件存储。
type History struct {
	dir    string
	mu     sync.Mutex
	open   map[string]*os.File
	closed bool
}

// NewHistory 创建聊天记录管理器（目录为空则不落盘）。
func NewHistory(dir string) *History {
	if dir == "" {
		return nil
	}
	_ = os.MkdirAll(dir, 0o755)
	return &History{dir: dir, open: make(map[string]*os.File)}
}

// Append 写一条记录。who 为 "我" 或对方昵称。
func (h *History) Append(peerIP, who, text string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	f, ok := h.open[peerIP]
	if !ok {
		path := filepath.Join(h.dir, sanitizeFilename(peerIP)+".log")
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		h.open[peerIP] = f
	}
	fmt.Fprintf(f, "[%s] %s: %s\n", time.Now().Format("2006-01-02 15:04:05"), who, strings.ReplaceAll(text, "\n", " "))
}

// LoadRecent 读取与某好友最近 maxLines 行记录。
func (h *History) LoadRecent(peerIP string, maxLines int) []string {
	if h == nil {
		return nil
	}
	path := filepath.Join(h.dir, sanitizeFilename(peerIP)+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// Close 关闭全部文件句柄，之后的 Append 静默忽略。
func (h *History) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, f := range h.open {
		f.Close()
	}
}

func sanitizeFilename(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "/", "_"), ":", "_")
}
