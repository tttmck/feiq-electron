package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pumpEvents 收集 core 事件直到条件满足或超时。
func pumpEvents(ch chan any, timeout time.Duration, match func(any) bool) []any {
	var got []any
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-ch:
			got = append(got, ev)
			if match(ev) {
				return got
			}
		case <-deadline:
			return got
		}
	}
}

func newTestCore(t *testing.T, nick string, port int) *Core {
	t.Helper()
	dir := t.TempDir()
	c := New(Config{
		Nickname:    nick,
		Port:        port,
		DataDir:     dir,
		DownloadDir: filepath.Join(dir, "dl"),
	})
	if err := c.Start(); err != nil {
		t.Fatalf("%s 启动失败: %v", nick, err)
	}
	return c
}

func TestDiscoveryAndChat(t *testing.T) {
	alice := newTestCore(t, "Alice", 24261)
	bob := newTestCore(t, "小波", 24262)
	defer func() { alice.Shutdown(); bob.Shutdown() }()

	// Alice 定向上线到 Bob（跨端口环境广播不可达，真实场景为广播）
	alice.PingEntryTo("127.0.0.1", 24262)

	sawOnline := false
	sawMsg := false
	for ev := range pumpEvents(bob.Events(), 3*time.Second, func(e any) bool {
		if _, ok := e.(MsgInEvent); ok {
			sawMsg = true
		}
		if pe, ok := e.(PalEvent); ok && !pe.Off && pe.Pal.Name == "Alice" {
			sawOnline = true
		}
		return sawOnline && sawMsg
	}) {
		_ = ev
		if sawOnline && sawMsg {
			break
		}
	}
	if !sawOnline {
		t.Fatal("Bob 未发现 Alice 上线")
	}

	// Bob 的好友表里应有 Alice，然后 Alice 发消息给 Bob
	pal := bob.Pals().All()
	var aliceIP string
	for _, p := range pal {
		if p.Name == "Alice" {
			aliceIP = p.IP
		}
	}
	if aliceIP == "" {
		t.Fatal("Bob 好友表中无 Alice")
	}
	alice.SendMessage([]string{"127.0.0.1"}, "你好，来自 Alice 的中文消息")
	gotMsg := false
	for _, ev := range pumpEvents(bob.Events(), 3*time.Second, func(e any) bool {
		m, ok := e.(MsgInEvent)
		return ok && m.Text == "你好，来自 Alice 的中文消息"
	}) {
		if m, ok := ev.(MsgInEvent); ok && m.Text == "你好，来自 Alice 的中文消息" {
			gotMsg = true
		}
	}
	if !gotMsg {
		t.Fatal("Bob 未收到消息")
	}
}

func TestFileTransferEndToEnd(t *testing.T) {
	sender := newTestCore(t, "发送者", 24263)
	receiver := newTestCore(t, "接收者", 24264)
	defer func() { sender.Shutdown(); receiver.Shutdown() }()

	sender.PingEntryTo("127.0.0.1", 24264)
	pumpEvents(receiver.Events(), 3*time.Second, func(e any) bool {
		_, ok := e.(PalEvent)
		return ok
	})
	// sender 通过 receiver 的 ANSENTRY 学到 127.0.0.1:24264
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && sender.Pals().Get("127.0.0.1") == nil {
		time.Sleep(50 * time.Millisecond)
	}
	if sender.Pals().Get("127.0.0.1") == nil {
		t.Fatal("sender 未通过 ANSENTRY 学到 receiver")
	}

	srcDir := t.TempDir()
	bigFile := filepath.Join(srcDir, "测试大文件.bin")
	content := make([]byte, 300*1024)
	for i := range content {
		content[i] = byte(i * 7)
	}
	os.WriteFile(bigFile, content, 0o644)
	subDir := filepath.Join(srcDir, "子目录")
	os.MkdirAll(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "内部.txt"), []byte("目录内文件内容"), 0o644)

	sender.SendFiles([]string{"127.0.0.1"}, []string{bigFile, subDir}, "请查收")

	// 接收方应收到通告
	var offer FileOfferEvent
	gotOffer := false
	for _, ev := range pumpEvents(receiver.Events(), 3*time.Second, func(e any) bool {
		o, ok := e.(FileOfferEvent)
		if ok {
			offer = o
			gotOffer = true
		}
		return ok
	}) {
		_ = ev
	}
	if !gotOffer {
		t.Fatal("接收方未收到文件通告")
	}
	if len(offer.Files) != 2 || offer.Files[0].Name != "测试大文件.bin" || offer.Files[1].Attr != 0x2 {
		t.Fatalf("通告内容不符: %+v", offer.Files)
	}

	receiver.AcceptFiles(offer.PacketNo, "")

	// 等传输完成
	doneFile := false
	doneDir := false
	pumpEvents(receiver.Events(), 10*time.Second, func(e any) bool {
		te, ok := e.(TransEvent)
		if !ok {
			return false
		}
		if te.State == "done" && te.Label == "测试大文件.bin" {
			doneFile = true
		}
		if te.State == "done" && te.Label == "子目录/" {
			doneDir = true
		}
		return doneFile && doneDir
	})
	if !doneFile || !doneDir {
		t.Fatalf("传输未完成: file=%v dir=%v", doneFile, doneDir)
	}

	// 校验文件内容
	got, err := os.ReadFile(filepath.Join(receiver.Cfg().DownloadDir, "测试大文件.bin"))
	if err != nil || len(got) != len(content) {
		t.Fatalf("大文件接收异常: %v, %d 字节", err, len(got))
	}
	for i := 0; i < len(got); i++ {
		if got[i] != content[i] {
			t.Fatalf("大文件内容损坏 @%d", i)
		}
	}
	inner, err := os.ReadFile(filepath.Join(receiver.Cfg().DownloadDir, "子目录", "内部.txt"))
	if err != nil || string(inner) != "目录内文件内容" {
		t.Fatalf("目录文件接收异常: %v %q", err, inner)
	}
}

func TestOfflineQueue(t *testing.T) {
	a := newTestCore(t, "离线发送方", 24265)
	b := newTestCore(t, "接收方B", 24266)
	defer func() { a.Shutdown(); b.Shutdown() }()

	// B 上线让 A 认识 B，然后模拟 B 下线
	b.PingEntryTo("127.0.0.1", 24265)
	pumpEvents(a.Events(), 3*time.Second, func(e any) bool { _, ok := e.(PalEvent); return ok })
	a.Pals().Offline("127.0.0.1")

	// A 发消息 → 应进离线队列
	a.SendMessage([]string{"127.0.0.1"}, "离线消息测试")

	// B 重新上线 → A 补发
	b.PingEntryTo("127.0.0.1", 24265)
	gotMsg := false
	pumpEvents(b.Events(), 5*time.Second, func(e any) bool {
		if m, ok := e.(MsgInEvent); ok && m.Text == "离线消息测试" {
			gotMsg = true
		}
		return gotMsg
	})
	if !gotMsg {
		t.Fatal("B 上线后未收到补发的离线消息")
	}
}

func TestChatHistoryPersist(t *testing.T) {
	a := newTestCore(t, "落盘A", 24275)
	b := newTestCore(t, "落盘B", 24276)
	defer func() { a.Shutdown(); b.Shutdown() }()

	// 互相发现
	a.PingEntryTo("127.0.0.1", 24276)
	pumpEvents(b.Events(), 3*time.Second, func(e any) bool { _, ok := e.(PalEvent); return ok })

	// A 发消息给 B（127.0.0.1）
	a.SendMessage([]string{"127.0.0.1"}, "要落盘的消息")
	pumpEvents(b.Events(), 3*time.Second, func(e any) bool {
		_, ok := e.(MsgInEvent)
		return ok
	})

	// 双方磁盘都应有记录
	time.Sleep(100 * time.Millisecond)
	aLines := a.HistoryLoad("127.0.0.1", 10)
	bLines := b.HistoryLoad("127.0.0.1", 10)
	foundA, foundB := false, false
	for _, l := range aLines {
		if strings.Contains(l, "我") && strings.Contains(l, "要落盘的消息") {
			foundA = true
		}
	}
	for _, l := range bLines {
		if strings.Contains(l, "落盘A") && strings.Contains(l, "要落盘的消息") {
			foundB = true
		}
	}
	if !foundA {
		t.Fatalf("发送方未落盘: %v", aLines)
	}
	if !foundB {
		t.Fatalf("接收方未落盘: %v", bLines)
	}
}
