package ipmsg

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// RecvPacket 是一次 UDP 收包及其来源。
type RecvPacket struct {
	Pkt  *Packet
	From *net.UDPAddr
}

// UDP 负责 2425 端口的报文收发与广播发现。
type UDP struct {
	conn *net.UDPConn
}

// ListenUDP 绑定端口并打开广播权限（SO_BROADCAST + SO_REUSEADDR）。
func ListenUDP(port int) (*UDP, error) {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: port}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("UDP 绑定 %d 失败: %w", port, err)
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		conn.Close()
		return nil, err
	}
	var sockErr error
	err = raw.Control(func(fd uintptr) {
		if e := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); e != nil {
			sockErr = e
		}
		if e := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); e != nil {
			sockErr = e
		}
	})
	if err != nil || sockErr != nil {
		conn.Close()
		return nil, fmt.Errorf("设置 UDP socket 选项失败: %v/%v", err, sockErr)
	}
	return &UDP{conn: conn}, nil
}

// Close 关闭 UDP socket。
func (u *UDP) Close() error { return u.conn.Close() }

// LocalAddr 返回本地监听地址。
func (u *UDP) LocalAddr() net.Addr { return u.conn.LocalAddr() }

// SendTo 向指定地址发送一条报文。
func (u *UDP) SendTo(ip string, port int, pkt *Packet) error {
	addr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
	if addr.IP == nil {
		return fmt.Errorf("无效地址: %s", ip)
	}
	data := EncodePacket(pkt)
	_, err := u.conn.WriteToUDP(data, addr)
	return err
}

// Broadcast 把报文发到 255.255.255.255 和本机所有 IPv4 接口的子网广播地址，
// 目的端口为本地监听端口。返回成功送达的目的地数量。
func (u *UDP) Broadcast(pkt *Packet) int {
	data := EncodePacket(pkt)
	port := DefaultPort
	if la, ok := u.conn.LocalAddr().(*net.UDPAddr); ok && la.Port != 0 {
		port = la.Port
	}
	sent := 0
	for _, ip := range BroadcastAddresses() {
		addr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
		if _, err := u.conn.WriteToUDP(data, addr); err == nil {
			sent++
		}
	}
	return sent
}

// BroadcastAddresses 返回需要覆盖的广播地址列表（全局广播 + 各子网定向广播）。
func BroadcastAddresses() []string {
	result := []string{"255.255.255.255"}
	seen := map[string]bool{"255.255.255.255": true}
	ifaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			bc := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bc[i] = ipnet.IP.To4()[i] | ^ipnet.Mask[i]
			}
			s := bc.String()
			if !seen[s] {
				seen[s] = true
				result = append(result, s)
			}
		}
	}
	return result
}

// Outbound 向指定地址发送已编码的原始报文（离线补发等场景）。
func (u *UDP) Outbound(ip string, port int, data []byte) error {
	addr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
	if addr.IP == nil {
		return fmt.Errorf("无效地址: %s", ip)
	}
	_, err := u.conn.WriteToUDP(data, addr)
	return err
}

// ReadPump 阻塞循环收包并投递到通道，直到 socket 关闭或出错。
func (u *UDP) ReadPump(ch chan<- *RecvPacket) {
	buf := make([]byte, 8192)
	for {
		n, from, err := u.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				close(ch)
				return
			}
			time.Sleep(10 * time.Millisecond) // 持续错误退避，避免忙转
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		pkt, err := DecodePacket(data)
		if err != nil {
			continue // 非 IPMSG 报文，忽略
		}
		if from == nil {
			continue
		}
		ch <- &RecvPacket{Pkt: pkt, From: from}
	}
}

// NewPacket 构造一条以本机身份发送的报文。
func NewPacket(no uint32, user, host string, cmd uint32, extra []byte) *Packet {
	return &Packet{
		Version:  "1",
		PacketNo: no,
		User:     user,
		Host:     host,
		Command:  cmd,
		Extra:    extra,
	}
}

// NextPacketNo 生成报文编号。
// 采用秒级 Unix 时间戳（协议惯例，32 位到 2038 年才回绕），
// 同秒内用原子计数器区分，保证严格递增。
var packetCounter atomic.Uint32
var packetLastSec atomic.Int64

// NextPacketNo 返回一个新的报文编号。
func NextPacketNo() uint32 {
	sec := time.Now().Unix()
	for {
		last := packetLastSec.Load()
		if last == sec {
			break
		}
		if packetLastSec.CompareAndSwap(last, sec) {
			packetCounter.Store(0) // 新的一秒，计数清零
			break
		}
	}
	return uint32(sec)*1000 + packetCounter.Add(1)
}

// Hostname 返回本机主机名（发送报文的 Host 字段）。
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "localhost"
	}
	return strings.ReplaceAll(h, ":", ";")
}

// LocalIPv4s 返回本机所有 IPv4 地址（用于回环过滤）。
func LocalIPv4s() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() {
				continue
			}
			out = append(out, ipnet.IP.To4().String())
		}
	}
	return out
}
