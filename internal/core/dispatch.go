package core

import (
	"fmt"
	"strconv"
	"strings"

	"feiq-tui/internal/ipmsg"
)

// ---- 事件类型（投递给 UI） ----

// PalEvent 好友上线/下线/更新。
type PalEvent struct {
	Pal *Pal
	Off bool // true=下线
}

// MsgInEvent 收到聊天消息。
type MsgInEvent struct {
	Pal       *Pal
	Text      string
	Broadcast bool // 是否为广播消息
}

// AckEvent 对方确认收到消息（送达回执）。
type AckEvent struct {
	IP string
}

// FileOfferEvent 对方发来附件通告，等待用户确认。
type FileOfferEvent struct {
	Pal      *Pal
	PacketNo uint32
	Files    []ipmsg.FileEntry
	Note     string
}

// TransEvent 文件传输进度。
// State: progress / done / error；Total<0 表示未知（目录）。
type TransEvent struct {
	Task  string
	Label string
	Done  int64
	Total int64
	State string
	Err   string
}

// NoticeEvent 系统提示。
type NoticeEvent struct {
	Text string
}

// handleUDP 分发一条收到的 UDP 报文。
func (c *Core) handleUDP(rp *ipmsg.RecvPacket) {
	pkt := rp.Pkt
	ip := rp.From.IP.String()
	fromPort := rp.From.Port

	if c.selfIPs[ip] { // 自己广播的回声
		return
	}
	if c.blacklist[ip] {
		return
	}

	switch ipmsg.Mode(pkt.Command) {
	case ipmsg.CmdBrEntry:
		c.upsertPort(ip, fromPort)
		pal := c.upsertFromEntry(pkt, ip)
		c.emit(PalEvent{Pal: pal})
		if !c.cfg.Stealth {
			extra := ipmsg.EncodeTextOut(c.nickname(), c.cfg.GBK)
			if c.cfg.Group != "" {
				extra = append(extra, 0)
				extra = append(extra, ipmsg.EncodeTextOut(c.cfg.Group, c.cfg.GBK)...)
			}
			_ = c.udp.SendTo(ip, c.peerPort(ip), ipmsg.NewPacket(ipmsg.NextPacketNo(),
				c.nickname(), ipmsg.Hostname(), ipmsg.CmdAnsEntry, extra))
		}
		c.flushOffline(ip)

	case ipmsg.CmdAnsEntry:
		c.upsertPort(ip, fromPort)
		pal := c.upsertFromEntry(pkt, ip)
		c.emit(PalEvent{Pal: pal})
		c.flushOffline(ip)

	case ipmsg.CmdBrExit:
		name := ipmsg.DecodeTextOpt(pkt.Command, []byte(pkt.User))
		if p := c.pals.Get(ip); p != nil {
			name = p.Name
		}
		if c.pals.Remove(ip) {
			c.emit(PalEvent{Off: true, Pal: &Pal{IP: ip, Name: name}})
		}

	case ipmsg.CmdBrAbsence:
		absence := pkt.Command&ipmsg.OptAbsence != 0
		pal := c.pals.Upsert(ip, func(p *Pal) {
			p.Name = ipmsg.DecodeText([]byte(pkt.User))
			p.Absence = absence
		})
		c.emit(PalEvent{Pal: pal})

	case ipmsg.CmdSendMsg:
		c.upsertPort(ip, fromPort)
		c.handleSendMsg(pkt, ip)

	case ipmsg.CmdRecvMsg:
		c.emit(AckEvent{IP: ip})

	case ipmsg.CmdGetInfo:
		_ = c.udp.SendTo(ip, c.peerPort(ip), ipmsg.NewPacket(ipmsg.NextPacketNo(),
			c.nickname(), ipmsg.Hostname(), ipmsg.CmdSendInfo|c.sendOpts(),
			ipmsg.EncodeTextOut(Version, c.cfg.GBK)))

	case ipmsg.CmdSendInfo:
		if p := c.pals.Upsert(ip, func(p *Pal) { p.Version = ipmsg.DecodeTextOpt(pkt.Command, pkt.Extra) }); p != nil {
			c.emit(PalEvent{Pal: c.pals.Get(ip)})
		}

	case ipmsg.CmdGetAbsenceInfo:
		_ = c.udp.SendTo(ip, c.peerPort(ip), ipmsg.NewPacket(ipmsg.NextPacketNo(),
			c.nickname(), ipmsg.Hostname(), ipmsg.CmdSendAbsenceInfo|c.sendOpts(),
			ipmsg.EncodeTextOut("离开", c.cfg.GBK)))

	case ipmsg.CmdReleaseFiles:
		// 对方取消接收；会话靠超时清理，无须处理

	case ipmsg.CmdReadMsg, ipmsg.CmdAnsReadMsg, ipmsg.CmdDelMsg,
		ipmsg.CmdNoOperation, ipmsg.CmdBrIsGetList, ipmsg.CmdOkGetList,
		ipmsg.CmdGetList, ipmsg.CmdAnsList, ipmsg.CmdBrIsGetList2:
		// 已读回执等暂不展示
	default:
		c.logger.Warn("未知命令", "cmd", fmt.Sprintf("0x%x", pkt.Command), "from", ip)
	}
}

// upsertFromEntry 从上线/应答/状态报文提取好友信息。
func (c *Core) upsertFromEntry(pkt *ipmsg.Packet, ip string) *Pal {
	segs := ipmsg.SplitExtra(pkt.Extra)
	name := ipmsg.DecodeTextOpt(pkt.Command, []byte(pkt.User))
	group := ""
	if len(segs) >= 2 {
		group = ipmsg.DecodeTextOpt(pkt.Command, []byte(segs[1]))
	}
	c.pals.Upsert(ip, func(p *Pal) {
		p.Name = name
		p.Host = ipmsg.DecodeTextOpt(pkt.Command, []byte(pkt.Host))
		p.Group = group
	})
	return c.pals.Get(ip)
}

// handleSendMsg 处理消息（含附件通告）。
func (c *Core) handleSendMsg(pkt *ipmsg.Packet, ip string) {
	// 陌生 IP 直接发消息：先登记为好友（飞秋行为一致）
	c.pals.Upsert(ip, func(p *Pal) {
		if p.Name == "" {
			p.Name = ipmsg.DecodeText([]byte(pkt.User))
			p.Host = ipmsg.DecodeTextOpt(pkt.Command, []byte(pkt.Host))
		}
	})
	// 送达回执优先，再判重播
	if pkt.Command&ipmsg.OptSendCheck != 0 {
		_ = c.udp.SendTo(ip, c.peerPort(ip), ipmsg.NewPacket(ipmsg.NextPacketNo(),
			c.nickname(), ipmsg.Hostname(), ipmsg.CmdRecvMsg,
			[]byte(strconv.FormatUint(uint64(pkt.PacketNo), 10))))
	}
	if c.pals.SeenPacket(ip, pkt.PacketNo) {
		return
	}

	segs := ipmsg.SplitExtra(pkt.Extra)
	text := ""
	if len(segs) >= 1 {
		text = ipmsg.DecodeTextOpt(pkt.Command, []byte(segs[0]))
	}
	broadcast := pkt.Command&ipmsg.OptBroadcast != 0

	if pkt.Command&ipmsg.OptFileAttach != 0 && len(segs) >= 2 {
		files, err := ipmsg.DecodeAttach(segs[1])
		if err != nil {
			c.emit(NoticeEvent{Text: "附件通告解析失败: " + err.Error()})
			return
		}
		c.mu.Lock()
		c.recvOffers[pkt.PacketNo] = &recvOffer{pal: c.pals.Get(ip), packetNo: pkt.PacketNo, files: files}
		c.mu.Unlock()
		c.emit(FileOfferEvent{Pal: c.pals.Get(ip), PacketNo: pkt.PacketNo, Files: files, Note: text})
	}

	if strings.TrimSpace(text) != "" || pkt.Command&ipmsg.OptFileAttach == 0 {
		pal := c.pals.Get(ip)
		if pal == nil {
			pal = &Pal{IP: ip, Name: ipmsg.DecodeTextOpt(pkt.Command, []byte(pkt.User)), Online: true}
		}
		if strings.TrimSpace(text) != "" {
			c.history.Append(ip, pal.Name, text)
		}
		c.emit(MsgInEvent{Pal: pal, Text: text, Broadcast: broadcast})
	}
}

// systemSession 是系统会话的保留键（通知/广播记录落盘于此）。
const systemSession = "_sys"


// upsertPort 学习对端端口。
func (c *Core) upsertPort(ip string, port int) {
	if port == 0 {
		return
	}
	c.pals.Upsert(ip, func(p *Pal) {
		if p.Port == 0 {
			p.Port = port
		}
	})
}

// peerPort 返回发往好友使用的端口。
func (c *Core) peerPort(ip string) int {
	if p := c.pals.Get(ip); p != nil && p.Port != 0 {
		return p.Port
	}
	return c.cfg.Port
}
