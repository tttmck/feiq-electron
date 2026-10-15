// Package core 实现 feiq-tui 的业务层：好友管理、消息收发、
// 文件传输会话与离线补发，并通过事件通道驱动 TUI。
package core

import (
	"sync"
	"time"
)

// Pal 表示一位局域网好友（以 IP 为唯一标识）。
type Pal struct {
	IP      string
	Name    string // 昵称（UTF-8）
	Host    string // 主机名
	Port    int    // 对方端口（从收包学习，默认 2425）
	Group   string // 分组（来自对方通告，可为空）
	Online  bool
	Absence bool // 离开状态
	Version string

	LastSeen time.Time
	lastSeq  uint32 // 最近一次处理的包号（去重）

	Unread int // 未读消息数（UI 用）
}

// DisplayName 返回列表展示名：昵称（主机名）。
func (p *Pal) DisplayName() string {
	if p.Host != "" && p.Host != p.Name {
		return p.Name + " (" + p.Host + ")"
	}
	return p.Name
}

// PalTable 是并发安全的好友表。
type PalTable struct {
	mu    sync.RWMutex
	pals  map[string]*Pal
	order []string // 上线顺序，用于稳定排序
}

// NewPalTable 创建好友表。
func NewPalTable() *PalTable {
	return &PalTable{pals: make(map[string]*Pal)}
}

// Upsert 插入或更新好友（按 IP），返回好友与是否新上线。
func (t *PalTable) Upsert(ip string, mut func(*Pal)) *Pal {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pals[ip]
	if !ok {
		p = &Pal{IP: ip, Online: true}
		t.pals[ip] = p
		t.order = append(t.order, ip)
	}
	if mut != nil {
		mut(p)
	}
	p.Online = true
	p.LastSeen = time.Now()
	return p
}

// Get 读取好友。
func (t *PalTable) Get(ip string) *Pal {
	t.mu.RLock()
	defer t.mu.RUnlock()
	p := t.pals[ip]
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// All 返回全部好友（拷贝），按组名+名字排序。
func (t *PalTable) All() []*Pal {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]*Pal, 0, len(t.pals))
	for _, ip := range t.order {
		if p, ok := t.pals[ip]; ok {
			cp := *p
			out = append(out, &cp)
		}
	}
	return out
}

// Offline 把好友标记下线。
func (t *PalTable) Offline(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pals[ip]
	if !ok {
		return false
	}
	p.Online = false
	p.Absence = false
	p.LastSeen = time.Now()
	return true
}

// Remove 删除好友（对方下线广播）。
func (t *PalTable) Remove(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.pals[ip]; !ok {
		return false
	}
	delete(t.pals, ip)
	for i, x := range t.order {
		if x == ip {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	return true
}

// CountOnline 返回在线好友数。
func (t *PalTable) CountOnline() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := 0
	for _, p := range t.pals {
		if p.Online {
			n++
		}
	}
	return n
}

// SeenPacket 记录包号做去重，返回是否为重复包。
func (t *PalTable) SeenPacket(ip string, no uint32) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pals[ip]
	if !ok {
		return false
	}
	// no<=lastSeq 视为重播；差值过大视为序号回绕，重新接受
	if no <= p.lastSeq && p.lastSeq != 0 && p.lastSeq-no < 0x40000000 {
		return true
	}
	p.lastSeq = no
	return false
}

// IncUnread 增加未读计数。
func (t *PalTable) IncUnread(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.pals[ip]; ok {
		p.Unread++
	}
}

// ClearUnread 清零未读。
func (t *PalTable) ClearUnread(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.pals[ip]; ok {
		p.Unread = 0
	}
}
