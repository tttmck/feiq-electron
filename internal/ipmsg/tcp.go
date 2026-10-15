package ipmsg

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FileProvider 由业务层实现：发送会话建立后，按 (packetID, fileID)
// 定位对方要拉取的文件。ok 为 false 表示会话不存在或已过期。
type FileProvider interface {
	ProvideFile(packetID, fileID uint32, remoteIP string) (path string, isDir bool, ok bool)
}

// ProgressFunc 周期性回调传输进度（累计完成字节数）。
type ProgressFunc func(done int64)

// remoteIP 提取连接对端 IP（无 IP 时返回空串）。
func remoteIP(conn net.Conn) string {
	if addr, ok := conn.RemoteAddr().(*net.TCPAddr); ok && addr.IP != nil {
		return addr.IP.String()
	}
	return ""
}

// TCPServer 监听 2425/TCP，响应 GETFILEDATA / GETDIRFILES / RELEASEFILES。
type TCPServer struct {
	ln       net.Listener
	provider FileProvider
}

// ListenTCP 启动 TCP 文件传输服务。
func ListenTCP(port int, provider FileProvider) (*TCPServer, error) {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("TCP 监听 %d 失败: %w", port, err)
	}
	return &TCPServer{ln: ln, provider: provider}, nil
}

// Close 关闭监听。
func (t *TCPServer) Close() error { return t.ln.Close() }

// Serve 接受连接循环，每连接一个 goroutine。
// 监听关闭才退出；临时错误（如 fd 耗尽）退避重试。
func (t *TCPServer) Serve() {
	for {
		conn, err := t.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go t.handle(conn)
	}
}

// handle 处理一条 TCP 文件请求。
// 请求与 UDP 报文同格式："1:no:user:host:cmd:packetID:fileID:offset"，
// 全 hex 附加段，按冒号段数截断，不依赖换行符（与 iptux/飞秋一致）。
func (t *TCPServer) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 读取请求：头部 5 个冒号 + extra 1~2 个冒号。
	// GETFILEDATA 的 extra 固定 3 段（2 冒号）；GETDIRFILES 按协议只需 2 段
	// （1 冒号），飞秋/iptux 实现可能多带一个尾 0 或行尾换行，都要兼容。
	buf := make([]byte, 0, 2048)
	tmp := make([]byte, 1024)
	need := 7 // 头 5 + extra 2
	for {
		colons := strings.Count(string(buf), ":")
		if colons >= need {
			break
		}
		// 已够解析头部时，若 GETDIRFILES 则不必等满 7 个冒号
		if colons >= 6 {
			if p, err := DecodePacket(buf); err == nil && Mode(p.Command) == CmdGetDirFiles {
				break
			}
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		}
		if err != nil || len(buf) > 2048 {
			return
		}
	}
	pkt, err := DecodePacket(buf)
	if err != nil {
		return
	}
	// 老实现可能在请求尾部带换行，剥离后再解析数值字段
	fields := strings.Split(strings.TrimRight(string(pkt.Extra), "\r\n"), ":")

	switch Mode(pkt.Command) {
	case CmdGetFileData:
		// packetID:fileID:offset
		if len(fields) < 3 {
			return
		}
		packetID, e1 := parseUint(fields[0], 16)
		fileID, e2 := parseUint(fields[1], 16)
		offset, e3 := strconv.ParseInt(fields[2], 16, 64)
		if e1 != nil || e2 != nil || e3 != nil {
			return
		}
		path, isDir, ok := t.provider.ProvideFile(packetID, fileID, remoteIP(conn))
		if !ok || isDir {
			return
		}
		t.serveRegular(conn, path, offset)
	case CmdGetDirFiles:
		// packetID:fileID[:0]
		if len(fields) < 2 {
			return
		}
		packetID, e1 := parseUint(fields[0], 16)
		fileID, e2 := parseUint(fields[1], 16)
		if e1 != nil || e2 != nil {
			return
		}
		path, isDir, ok := t.provider.ProvideFile(packetID, fileID, remoteIP(conn))
		if !ok || !isDir {
			return
		}
		_ = sendDirStream(deadlineWriter{conn: conn}, path, nil)
	case CmdReleaseFiles:
		// 取消接收，业务层无须额外动作：连接关闭即放弃
	default:
	}
}

// serveRegular 从 offset 起把文件原始字节推给对端（无文件头）。
func (t *TCPServer) serveRegular(conn net.Conn, path string, offset int64) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return
		}
	}
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	buf := make([]byte, 64*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		}
		if rerr != nil {
			return
		}
	}
}

// ---- 客户端（拉取方） ----

// BuildFileRequest 构造 GETFILEDATA 请求报文的附加段：packetID:fileID:offset（hex）。
func BuildFileRequest(packetID, fileID uint32, offset int64) []byte {
	return []byte(fmt.Sprintf("%x:%x:%x", packetID, fileID, offset))
}

// BuildDirRequest 构造 GETDIRFILES 请求报文的附加段：packetID:fileID:0。
func BuildDirRequest(packetID, fileID uint32) []byte {
	return []byte(fmt.Sprintf("%x:%x:0", packetID, fileID))
}

// SendFileRequest 向 peer 的 TCP 端口发送一条文件请求报文并返回连接。
// 对端（iptux/飞秋）读到足够字段即处理，无换行依赖。
func SendFileRequest(ip string, port int, pkt *Packet) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("%s:%d", ip, port), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接 %s:%d 失败: %w", ip, port, err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(EncodePacket(pkt)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// PullFile 拉取单文件：发 GETFILEDATA，按 size-offset 收满原始字节。
func PullFile(ip string, port int, user, host string, packetID, fileID uint32,
	offset, size int64, dst io.Writer, prog ProgressFunc) (int64, error) {

	cmd := CmdGetFileData // 对非 iptux 客户端按 iptux 惯例不加选项标志
	pkt := NewPacket(NextPacketNo(), user, host, cmd, BuildFileRequest(packetID, fileID, offset))
	conn, err := SendFileRequest(ip, port, pkt)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	return copyWithDeadline(conn, dst, size-offset, prog)
}

// copyWithDeadline 从 conn 读取 exact 字节写入 dst，每 30 秒无数据则超时。
func copyWithDeadline(conn net.Conn, dst io.Writer, exact int64, prog ProgressFunc) (int64, error) {
	buf := make([]byte, 64*1024)
	var done int64
	last := time.Now()
	for done < exact {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		want := int64(len(buf))
		if exact-done < want {
			want = exact - done
		}
		n, err := conn.Read(buf[:want])
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if prog != nil && time.Since(last) > 300*time.Millisecond {
				prog(done)
				last = time.Now()
			}
		}
		if err != nil {
			if done >= exact {
				break
			}
			return done, fmt.Errorf("读取中断（完成 %d/%d）: %w", done, exact, err)
		}
	}
	if prog != nil {
		prog(done)
	}
	return done, nil
}

// deadlineWriter 每次写前续 30 秒写超时，防慢速对端永久挂住。
type deadlineWriter struct {
	conn net.Conn
}

func (d deadlineWriter) Write(b []byte) (int, error) {
	_ = d.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return d.conn.Write(b)
}

// ---- 目录流（发送侧） ----

// sendDirStream 按 IPMSG 目录流格式深度优先遍历 root 并写入 w。
// nameEnc 为 nil 时文件名按 GBK 编码输出（兼容 Windows 客户端）。
// 格式：每项 "XXXX:name:size:attr:14=mtime:16=ctime:" + data，
// XXXX 为头部长度（4 位 hex）；目录递归，结尾每层发 RETPARENT 项。
func sendDirStream(w io.Writer, root string, nameEnc func(string) []byte) error {
	if nameEnc == nil {
		nameEnc = func(s string) []byte { return EncodeTextOut(s, true) }
	}
	writeHeader := func(name string, size int64, attr uint32, mtime int64) error {
		head := fmt.Sprintf("0000:%s:%x:%x:14=%x:16=%x:",
			strings.ReplaceAll(string(nameEnc(name)), ":", "::"),
			size, attr, mtime, mtime)
		h := fmt.Sprintf("%04x", len(head))
		head = h + head[4:]
		_, err := io.WriteString(w, head)
		return err
	}
	sendOne := func(path string, info os.FileInfo) error {
		var attr uint32 = FileRegular
		var size int64
		if info.IsDir() {
			attr = FileDir
		} else {
			size = info.Size()
		}
		if err := writeHeader(info.Name(), size, attr, info.ModTime().Unix()); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.CopyN(w, f, size)
		return err
	}
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				info, err := e.Info()
				if err != nil {
					continue
				}
				if err := sendOne(full, info); err != nil {
					return err
				}
				if err := walk(full); err != nil {
					return err
				}
				if err := writeHeader(".", 0, FileRetParent, info.ModTime().Unix()); err != nil {
					return err
				}
			} else {
				info, err := e.Info()
				if err != nil {
					continue
				}
				if err := sendOne(full, info); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// 根目录自身的头 + 内容 + 返回根的 RETPARENT
	rootInfo, err := os.Stat(root)
	if err != nil {
		return err
	}
	if err := writeHeader(rootInfo.Name(), 0, FileDir, rootInfo.ModTime().Unix()); err != nil {
		return err
	}
	if err := walk(root); err != nil {
		return err
	}
	return writeHeader(".", 0, FileRetParent, rootInfo.ModTime().Unix())
}

// ---- 目录流（接收侧） ----

// PullDir 拉取整个目录并展开到 destDir 下，返回总接收字节数。
func PullDir(ip string, port int, user, host string, packetID, fileID uint32,
	destDir string, prog ProgressFunc) (int64, error) {

	pkt := NewPacket(NextPacketNo(), user, host, CmdGetDirFiles, BuildDirRequest(packetID, fileID))
	conn, err := SendFileRequest(ip, port, pkt)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return RecvDirStream(conn, destDir, prog)
}

// touchRead 续 30 秒读超时（目录流全程防挂）。
func touchRead(conn net.Conn) {
	if c, ok := conn.(net.Conn); ok {
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	}
}

// RecvDirStream 解析对端推来的目录流并落盘（兼容 GBK/UTF-8 文件名）。
// r 应为 net.Conn，每轮续读超时防止对端停滞时永久挂起。
func RecvDirStream(r io.Reader, destDir string, prog ProgressFunc) (int64, error) {
	if conn, ok := r.(net.Conn); ok {
		touchRead(conn)
	}
	br := bufio.NewReaderSize(r, 64*1024)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, err
	}
	// dirStack[0] 为根（destDir 本身）
	dirStack := []string{destDir}
	var total int64

	for {
		if conn, ok := r.(net.Conn); ok {
			touchRead(conn)
		}
		head, err := readDirHeader(br)
		if err != nil {
			if err == io.EOF && len(dirStack) == 1 {
				return total, nil
			}
			return total, err
		}
		switch Mode(head.attr) {
		case FileRetParent:
			if len(dirStack) > 1 {
				dirStack = dirStack[:len(dirStack)-1]
			} else {
				return total, nil // 回到根，结束
			}
		case FileDir:
			sub := filepath.Join(dirStack[len(dirStack)-1], head.name)
			if err := os.MkdirAll(sub, 0o755); err != nil {
				return total, err
			}
			dirStack = append(dirStack, sub)
		case FileRegular:
			dst := filepath.Join(dirStack[len(dirStack)-1], head.name)
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return total, err
			}
			n, cpErr := copyNFrom(br, f, head.size)
			f.Close()
			total += n
			if prog != nil {
				prog(total)
			}
			if cpErr != nil {
				return total, cpErr
			}
		default:
			// 未知类型（符号链接等）：按大小丢弃数据
			if _, err := io.CopyN(io.Discard, br, head.size); err != nil {
				return total, err
			}
		}
	}
}

type dirHeader struct {
	name string
	size int64
	attr uint32
}

// readDirHeader 从流中读一个完整目录流头并消费掉。
// 头格式 "XXXX:name:size:attr[:14=mtime:16=ctime]:"，XXXX 为头字节长度。
func readDirHeader(br *bufio.Reader) (*dirHeader, error) {
	for {
		buf, _ := br.Peek(br.Buffered())
		idx := bytesIndexByte(buf, ':')
		if idx == 0 {
			return nil, fmt.Errorf("目录流头以':'开头，格式非法")
		}
		if idx > 0 {
			hs, err := strconv.ParseUint(string(buf[:idx]), 16, 32)
			if err != nil {
				return nil, fmt.Errorf("目录流头长度字段非十六进制: %q", buf[:idx])
			}
			if int(hs) <= len(buf) {
				h, perr := parseDirHeader(buf[:hs])
				if perr != nil {
					return nil, perr
				}
				if _, derr := br.Discard(int(hs)); derr != nil {
					return nil, derr
				}
				return h, nil
			}
			// 头尚不完整：Peek(hs) 强制从底层连接读取
			if _, ferr := br.Peek(int(hs)); ferr != nil {
				return nil, ferr
			}
			continue
		}
		// 缓冲内无冒号：强制多读 1 字节（Peek(Buffered()+1) 必触发底层读）
		if _, err := br.Peek(br.Buffered() + 1); err != nil {
			return nil, err
		}
	}
}

func parseDirHeader(head []byte) (*dirHeader, error) {
	if len(head) == 0 || head[len(head)-1] != ':' {
		return nil, fmt.Errorf("目录流头格式错误: %q", head)
	}
	s := string(head)
	pos := 0
	var raw string
	var err error

	// 跳过长度字段
	if _, pos, err = nextField(s, pos); err != nil {
		return nil, err
	}
	nameRaw, np, err := nextField(s, pos)
	if err != nil {
		return nil, err
	}
	pos = np
	if raw, pos, err = nextField(s, pos); err != nil {
		return nil, err
	}
	size, err := parseInt(raw, 16)
	if err != nil {
		return nil, err
	}
	if raw, pos, err = nextField(s, pos); err != nil {
		return nil, err
	}
	attr, err := parseUint(raw, 16)
	if err != nil {
		return nil, err
	}
	name := DecodeText([]byte(unescapeName(nameRaw)))
	_ = pos // 其余扩展段（mtime 等）暂不使用
	return &dirHeader{name: name, size: size, attr: attr}, nil
}

// copyNFrom 与 io.CopyN 相同，但源为 bufio.Reader 时不破坏缓冲。
func copyNFrom(br *bufio.Reader, dst io.Writer, n int64) (int64, error) {
	return io.CopyN(dst, br, n)
}

func bytesIndexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
