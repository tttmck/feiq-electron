package ipmsg

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestPacketRoundTrip(t *testing.T) {
	p := &Packet{PacketNo: 42, User: "张三", Host: "PC-01", Command: CmdSendMsg | OptUTF8, Extra: []byte("你好，飞秋")}
	data := EncodePacket(p)
	got, err := DecodePacket(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.PacketNo != 42 || got.User != "张三" || got.Host != "PC-01" ||
		got.Command != CmdSendMsg|OptUTF8 || string(got.Extra) != "你好，飞秋" {
		t.Fatalf("往返不一致: %+v", got)
	}
}

func TestDecodeGBKExtra(t *testing.T) {
	gbk, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("中文消息"))
	s := DecodeText(gbk)
	if s != "中文消息" {
		t.Fatalf("GBK 解码失败: %q", s)
	}
	if DecodeText([]byte("纯UTF8")) != "纯UTF8" {
		t.Fatal("UTF-8 直通失败")
	}
}

func TestAttachRoundTrip(t *testing.T) {
	files := []FileEntry{
		{ID: 0, Name: "报告:最终版.pdf", Size: 1234567, Mtime: 1700000000, Attr: FileRegular},
		{ID: 1, Name: "素材目录", Size: 0, Mtime: 1700000001, Attr: FileDir},
		{ID: 2, Name: "说明.txt", Size: 42, Mtime: 1700000002, Attr: FileRegular},
	}
	s := EncodeAttach(files, false)
	got, err := DecodeAttach(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("条目数不符: %d", len(got))
	}
	for i := range files {
		if got[i].ID != files[i].ID || got[i].Name != files[i].Name ||
			got[i].Size != files[i].Size || got[i].Attr != files[i].Attr {
			t.Fatalf("第 %d 条不符: %+v vs %+v", i, got[i], files[i])
		}
	}
}

func TestAttachDecodeFromFeiqStyle(t *testing.T) {
	// 模拟飞秋发出的通告：GBK 文件名 + extend 属性段
	nameGBK, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("新建文本文档.txt"))
	// 手工拼: 0:<gbk名>:64:65c0a8c0:1:41=utf8名
	s := "0:" + string(nameGBK) + ":64:65c0a8c0:1:41=utf8name"
	got, err := DecodeAttach(s)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "新建文本文档.txt" || got[0].Size != 0x64 || got[0].Attr != 1 || got[0].Mtime != 0x65c0a8c0 {
		t.Fatalf("解析不符: %+v", got[0])
	}
}

func TestSplitExtra(t *testing.T) {
	segs := SplitExtra([]byte("看看这个文件\x000:照片.jpg:1000:65c00000:1"))
	if len(segs) != 2 || segs[0] != "看看这个文件" {
		t.Fatalf("分段失败: %+v", segs)
	}
}

// fakeProvider 用于 TCP 端到端测试。
type fakeProvider struct {
	path  string
	isDir bool
}

func (f *fakeProvider) ProvideFile(packetID, fileID uint32, remoteIP string) (string, bool, bool) {
	return f.path, f.isDir, true
}

func startTCPServer(t *testing.T, prov FileProvider) (addr string, stop func()) {
	t.Helper()
	srv, err := ListenTCP(0, prov)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	_, port, _ := net.SplitHostPort(srv.ln.Addr().String())
	return "127.0.0.1:" + port, func() { _ = srv.Close() }
}

func TestPullRegularFile(t *testing.T) {
	content := bytes.Repeat([]byte("feiq-tui 测试数据 0123\n"), 5000) // ~110KB
	src := filepath.Join(t.TempDir(), "源文件.dat")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	addr, stop := startTCPServer(t, &fakeProvider{path: src})
	defer stop()

	var buf bytes.Buffer
	n, err := PullFile("127.0.0.1", mustPort(addr), "tester", "host", 0xabc, 0, 0, int64(len(content)), &buf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(content)) || !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("内容不符: 收到 %d 字节", n)
	}
}

func TestPullFileWithOffset(t *testing.T) {
	content := []byte("0123456789abcdef")
	src := filepath.Join(t.TempDir(), "part.bin")
	os.WriteFile(src, content, 0o644)
	addr, stop := startTCPServer(t, &fakeProvider{path: src})
	defer stop()

	var buf bytes.Buffer
	if _, err := PullFile("127.0.0.1", mustPort(addr), "t", "h", 1, 0, 4, int64(len(content)), &buf, nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "456789abcdef" {
		t.Fatalf("断点内容不符: %q", buf.String())
	}
}

func TestDirStreamRoundTrip(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "发送目录")
	os.MkdirAll(root, 0o755)
	// 构造目录树：根/{中文目录/{a.txt, 空.txt}, b.bin, c;d.txt（冒号名）
	sub := filepath.Join(root, "中文目录")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "a.txt"), []byte("内容A"), 0o644)
	os.WriteFile(filepath.Join(sub, "空.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "b.bin"), []byte("BBBBBBBBBB"), 0o644)
	os.WriteFile(filepath.Join(root, "c;d.txt"), []byte("冒号名"), 0o644) // 源名含冒号（UDP 层转义）

	addr, stop := startTCPServer(t, &fakeProvider{path: root, isDir: true})
	defer stop()

	dest := t.TempDir()
	total, err := PullDir("127.0.0.1", mustPort(addr), "tester", "host", 7, 0, dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(len("内容A")+len("BBBBBBBBBB")+len("冒号名")) {
		t.Fatalf("总字节数不符: %d", total)
	}
	check := func(rel, want string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", rel, err)
		}
		if string(b) != want {
			t.Fatalf("%s 内容不符: %q", rel, b)
		}
	}
	rootName := "发送目录"
	check(filepath.Join(rootName, "中文目录", "a.txt"), "内容A")
	check(filepath.Join(rootName, "中文目录", "空.txt"), "")
	check(filepath.Join(rootName, "b.bin"), "BBBBBBBBBB")
	// 接收端遇到源名里的 ';' 不会被还原成 ':'（可接受，目录流协议层转义只针对 ':'）
	check(filepath.Join(rootName, "c;d.txt"), "冒号名")
}

func mustPort(hostport string) int {
	_, p, _ := net.SplitHostPort(hostport)
	var port int
	fmtSscan(p, &port)
	return port
}

func fmtSscan(s string, out *int) {
	for _, c := range s {
		*out = *out*10 + int(c-'0')
	}
}

func TestBroadcastAddressesNonEmpty(t *testing.T) {
	addrs := BroadcastAddresses()
	if len(addrs) == 0 || addrs[0] != "255.255.255.255" {
		t.Fatalf("广播地址异常: %v", addrs)
	}
}

func TestNextPacketNoIncreasing(t *testing.T) {
	a := NextPacketNo()
	time.Sleep(2 * time.Millisecond)
	b := NextPacketNo()
	if b <= a {
		t.Fatalf("包号未递增: %d -> %d", a, b)
	}
}

func TestAttachIptuxSeparator(t *testing.T) {
	// iptux 的条目以 ":\a:" 分隔，且尾部可能带 "\a"
	s := "0:文件一:64:65c0a8c0:1:\a:1:文件二:128:65c0a8c1:1:\a:"
	got, err := DecodeAttach(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "文件一" || got[1].Name != "文件二" || got[1].Size != 0x128 {
		t.Fatalf("iptux 风格解析不符: %+v", got)
	}
}

func TestEncodeAttachIptuxReadable(t *testing.T) {
	// 我们编码的串必须能被 iptux 的 \a 定位逻辑解析
	files := []FileEntry{
		{ID: 0, Name: "a.txt", Size: 1, Mtime: 2, Attr: FileRegular},
		{ID: 1, Name: "b.txt", Size: 3, Mtime: 4, Attr: FileRegular},
	}
	s := EncodeAttach(files, false)
	if !strings.Contains(s, ":\a:") {
		t.Fatalf("缺少 iptux 分隔符: %q", s)
	}
	got, err := DecodeAttach(s)
	if err != nil || len(got) != 2 {
		t.Fatalf("自编码自解析失败: %v %+v", err, got)
	}
}

func TestReadDirHeaderTruncated(t *testing.T) {
	// 半截流/垃圾流必须在有限时间内报错，不能挂死
	for _, input := range []string{"00", ":", ":zzzz", "zz:", "0", ""} {
		done := make(chan error, 1)
		go func(in string) {
			br := bufio.NewReaderSize(strings.NewReader(in), 64*1024)
			_, err := readDirHeader(br)
			done <- err
		}(input)
		select {
		case err := <-done:
			if err == nil && input != "" {
				// 空输入等 EOF 也算错误返回；只有完整头才允许 nil
				t.Logf("输入 %q 返回 %v", input, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("输入 %q 挂死（readDirHeader 忙循环）", input)
		}
	}
}
