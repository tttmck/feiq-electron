package ipmsg

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// DefaultPort 是 IPMSG 协议的默认端口，UDP 与 TCP 共用。
const DefaultPort = 2425

// Packet 表示一条 IPMSG 报文。Extra 保留原始字节，
// 因为对端可能用 UTF-8 或 GBK 编码，需按上下文分别解码。
type Packet struct {
	Version  string
	PacketNo uint32
	User     string // 发送者昵称
	Host     string // 发送者主机名
	Command  uint32
	Extra    []byte // 附加段原始字节
}

// EncodePacket 按协议格式序列化报文，文本字段统一采用 UTF-8。
// 调用方负责在 Command 中带上 OptUTF8 标志。
func EncodePacket(p *Packet) []byte {
	var b strings.Builder
	b.WriteString("1:")
	b.WriteString(strconv.FormatUint(uint64(p.PacketNo), 10))
	b.WriteByte(':')
	b.WriteString(sanitizeText(p.User))
	b.WriteByte(':')
	b.WriteString(sanitizeText(p.Host))
	b.WriteByte(':')
	b.WriteString(fmt.Sprintf("%d", p.Command))
	b.WriteByte(':')
	b.Write(p.Extra)
	return []byte(b.String())
}

// DecodePacket 从原始字节解析报文。只按冒号切分前 5 段，
// 其余全部归入 Extra，不做任何文本解码。
func DecodePacket(data []byte) (*Packet, error) {
	// 依次定位 5 个冒号
	idx := make([]int, 0, 6)
	for i, c := range data {
		if c == ':' {
			idx = append(idx, i)
			if len(idx) == 5 {
				break
			}
		}
	}
	if len(idx) < 5 {
		return nil, fmt.Errorf("报文字段不足: %q", truncate(data, 60))
	}
	p := &Packet{Version: string(data[:idx[0]])}
	no, err := strconv.ParseUint(string(data[idx[0]+1:idx[1]]), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("包号解析失败: %w", err)
	}
	p.PacketNo = uint32(no)
	p.User = string(data[idx[1]+1 : idx[2]])
	p.Host = string(data[idx[2]+1 : idx[3]])
	cmd, err := strconv.ParseUint(string(data[idx[3]+1 : idx[4]]), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("命令字解析失败: %w", err)
	}
	p.Command = uint32(cmd)
	if len(data) > idx[4]+1 {
		p.Extra = data[idx[4]+1:]
	}
	return p, nil
}

// DecodeText 把对端发来的文本解码为 Go 字符串：
// 优先按 UTF-8（utf8.Valid），否则按 GB18030，仍失败则替换无效字节。
func DecodeText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if s, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b); err == nil {
		return string(s)
	}
	return strings.ToValidUTF8(string(b), "?")
}

// DecodeTextOpt 按命令字的编码标志解码文本：
// 对方带 OptUTF8 时直接按 UTF-8（无效字节替换），不带时嗅探回退 GB18030。
func DecodeTextOpt(command uint32, b []byte) string {
	if command&OptUTF8 != 0 {
		return strings.ToValidUTF8(string(b), "?")
	}
	return DecodeText(b)
}

// EncodeTextOut 按发送策略编码文本：默认 UTF-8；
// gbk 为 true 时编码为 GB18030（GBK 超集且全 Unicode 可表，
// 兼容不支持 UTF8OPT 的老客户端）。
func EncodeTextOut(s string, gbk bool) []byte {
	if !gbk {
		return []byte(s)
	}
	if b, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(s)); err == nil {
		return b
	}
	return []byte(s)
}

// SplitExtra 按 NUL 把附加段切成消息文本与附件串等若干段。
func SplitExtra(extra []byte) []string {
	return strings.Split(string(extra), "\x00")
}

// sanitizeText 清理文本字段中协议不允许的字符（冒号、换行）。
func sanitizeText(s string) string {
	s = strings.ReplaceAll(s, ":", ";")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
