package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"feiq-tui/internal/ipmsg"
)

// Config 是启动配置。
type Config struct {
	Nickname    string
	Group       string // 我的分组（通告给对方）
	Port        int
	GBK         bool   // 发送侧强制 GBK 编码（兼容老客户端）
	Stealth     bool   // 隐身：不上线广播，不回应答
	DownloadDir string // 接收文件保存目录
	DataDir     string // ~/.feiq-tui
	Blacklist   []string
	ProbeNets   []string // 额外主动探测的网段（CIDR），用于无广播能力的网络
}

// Version 是对 GETINFO 的应答身份串。
const Version = "feiq-tui/1.0 (IPMSG compatible)"

// sendSession 是我方发起的文件发送会话，等对方主动 TCP 来拉。
type sendSession struct {
	packetNo  uint32
	files     []ipmsg.FileEntry
	paths     []string // paths[fileID] = 本地路径
	peers     map[string]bool // 允许来拉的来源 IP
	label     string
	createdAt time.Time
}

// recvOffer 是对方发来的附件通告（待确认）。
type recvOffer struct {
	pal       *Pal
	packetNo  uint32
	files     []ipmsg.FileEntry
	createdAt time.Time
}

type offlineMsg struct {
	ip   string
	data []byte // 已编码的完整报文
}

// Core 是业务核心：驱动 UDP/TCP，维护好友与传输会话，向 UI 发事件。
type Core struct {
	cfg     Config
	udp     *ipmsg.UDP
	tcp     *ipmsg.TCPServer
	pals    *PalTable
	events  chan any
	history *History
	logger  *slog.Logger

	mu           sync.Mutex
	sendSessions map[uint32]*sendSession
	recvOffers   map[uint32]*recvOffer
	offline      map[string][]offlineMsg
	blacklist    map[string]bool
	selfIPs      map[string]bool
	probedAt     map[string]time.Time // 网段扫描冷却
}

// New 创建核心（不启动网络）。
func New(cfg Config) *Core {
	if cfg.Port == 0 {
		cfg.Port = ipmsg.DefaultPort
	}
	if cfg.Nickname == "" {
		cfg.Nickname = defaultNickname()
	}
	c := &Core{
		cfg:          cfg,
		pals:         NewPalTable(),
		events:       make(chan any, 128),
		sendSessions: make(map[uint32]*sendSession),
		recvOffers:   make(map[uint32]*recvOffer),
		offline:      make(map[string][]offlineMsg),
		blacklist:    make(map[string]bool),
		selfIPs:      make(map[string]bool),
		logger:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	for _, ip := range cfg.Blacklist {
		c.blacklist[ip] = true
	}
	for _, ip := range ipmsg.LocalIPv4s() {
		c.selfIPs[ip] = true
	}
	c.history = NewHistory(filepath.Join(cfg.DataDir, "history"))
	return c
}

// Events 返回事件通道（UI 消费）。
func (c *Core) Events() chan any { return c.events }

// Pals 返回好友表。
func (c *Core) Pals() *PalTable { return c.pals }

// Cfg 返回配置副本。
func (c *Core) Cfg() Config { return c.cfg }

func defaultNickname() string {
	h, _ := os.Hostname()
	u := os.Getenv("USER")
	if u == "" {
		u = "me"
	}
	if h != "" {
		return u + "@" + h
	}
	return u
}

// Start 启动网络：UDP 收泵、TCP 服务、上线广播。
func (c *Core) Start() error {
	udp, err := ipmsg.ListenUDP(c.cfg.Port)
	if err != nil {
		return err
	}
	c.udp = udp
	tcp, err := ipmsg.ListenTCP(c.cfg.Port, c)
	if err != nil {
		udp.Close()
		return err
	}
	c.tcp = tcp
	pkts := make(chan *ipmsg.RecvPacket, 64)
	go udp.ReadPump(pkts)
	go c.consume(pkts)
	go tcp.Serve()
	go c.gcLoop()
	return nil
}

// consume 消费 UDP 收包并分发。
func (c *Core) consume(ch <-chan *ipmsg.RecvPacket) {
	for rp := range ch {
		c.handleUDP(rp)
	}
}

// gcLoop 定期清理过期的发送会话（对方始终不来拉取），
// 并周期性主动探测无广播能力的网段（好友后上线也能被发现）。
func (c *Core) gcLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		go c.probePeers()
		c.mu.Lock()
		for no, s := range c.sendSessions {
			if time.Since(s.createdAt) > 30*time.Minute {
				delete(c.sendSessions, no)
			}
		}
		for no, o := range c.recvOffers {
			if time.Since(o.createdAt) > 30*time.Minute {
				delete(c.recvOffers, no)
			}
		}
		c.mu.Unlock()
	}
}

// emit 投递事件：普通事件满了就丢（日志类），
// 消息/文件/传输类关键事件最多等 5 秒，避免 UI 短暂繁忙时静默丢消息。
func (c *Core) emit(ev any) {
	select {
	case c.events <- ev:
		return
	default:
	}
	switch ev.(type) {
	case MsgInEvent, FileOfferEvent, TransEvent, PalEvent:
		select {
		case c.events <- ev:
		case <-time.After(5 * time.Second):
			c.logger.Warn("事件投递超时丢弃", "type", fmt.Sprintf("%T", ev))
		}
	}
}

// ---- FileProvider（TCP 服务端回调） ----

// ProvideFile 按报文号+文件号定位待发送文件，并校验请求方是否为通告对象。
func (c *Core) ProvideFile(packetID, fileID uint32, remoteIP string) (string, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sendSessions[packetID]
	if !ok || int(fileID) >= len(s.paths) {
		return "", false, false
	}
	if !s.peers[remoteIP] {
		return "", false, false
	}
	return s.paths[fileID], s.files[fileID].Attr == ipmsg.FileDir, true
}

// HistoryAppend 落盘一条聊天记录（UI 调用）。
func (c *Core) HistoryAppend(ip, who, text string) { c.history.Append(ip, who, text) }

// HistoryLoad 读取与某好友的最近记录（UI 调用）。
func (c *Core) HistoryLoad(ip string, n int) []string { return c.history.LoadRecent(ip, n) }

// Shutdown 下线广播并关闭网络。
func (c *Core) Shutdown() {
	if !c.cfg.Stealth {
		c.udp.Broadcast(ipmsg.NewPacket(ipmsg.NextPacketNo(),
			c.nickname(), ipmsg.Hostname(), ipmsg.CmdBrExit,
			ipmsg.EncodeTextOut(c.nickname(), c.cfg.GBK)))
	}
	if c.udp != nil {
		c.udp.Close()
	}
	if c.tcp != nil {
		c.tcp.Close()
	}
	c.history.Close()
}

// BroadcastEntry 上线广播（刷新好友也复用）。
func (c *Core) BroadcastEntry() {
	if c.cfg.Stealth {
		return
	}
	extra := ipmsg.EncodeTextOut(c.nickname(), c.cfg.GBK)
	if c.cfg.Group != "" {
		extra = append(extra, 0)
		extra = append(extra, ipmsg.EncodeTextOut(c.cfg.Group, c.cfg.GBK)...)
	}
	c.udp.Broadcast(ipmsg.NewPacket(ipmsg.NextPacketNo(),
		c.nickname(), ipmsg.Hostname(), ipmsg.CmdBrEntry, extra))
	// 广播覆盖不到的网段（Tailscale 等 point-to-point 隧道）主动探测
	go c.probePeers()
}

// PingEntryTo 向指定地址单播上线通告（添加指定 IP 好友 / 跨网段探测）。
func (c *Core) PingEntryTo(ip string, port int) {
	if port == 0 {
		port = c.cfg.Port
	}
	extra := ipmsg.EncodeTextOut(c.nickname(), c.cfg.GBK)
	if c.cfg.Group != "" {
		extra = append(extra, 0)
		extra = append(extra, ipmsg.EncodeTextOut(c.cfg.Group, c.cfg.GBK)...)
	}
	_ = c.udp.SendTo(ip, port, ipmsg.NewPacket(ipmsg.NextPacketNo(),
		c.nickname(), ipmsg.Hostname(), ipmsg.CmdBrEntry, extra))
}

// SetAbsence 切换离开状态并广播。
func (c *Core) SetAbsence(on bool, note string) {
	cmd := ipmsg.CmdBrAbsence
	if on {
		cmd |= ipmsg.OptAbsence
	}
	extra := ipmsg.EncodeTextOut(c.nickname(), c.cfg.GBK)
	if c.cfg.Group != "" {
		extra = append(extra, 0)
		extra = append(extra, ipmsg.EncodeTextOut(c.cfg.Group, c.cfg.GBK)...)
	}
	c.udp.Broadcast(ipmsg.NewPacket(ipmsg.NextPacketNo(),
		c.nickname(), ipmsg.Hostname(), cmd, extra))
}

func (c *Core) nickname() string { return c.cfg.Nickname }

func (c *Core) sendOpts() uint32 {
	if c.cfg.GBK {
		return 0
	}
	return ipmsg.OptUTF8
}

// SendMessage 向多个好友发送文本；offline 队列在对方不在线时自动生效。
func (c *Core) SendMessage(ips []string, text string) {
	for _, ip := range ips {
		c.sendTo(ip, text)
	}
}

// BroadcastText 向全网段广播文本消息。记录落盘到系统会话。
func (c *Core) BroadcastText(text string) {
	cmd := ipmsg.CmdSendMsg | ipmsg.OptBroadcast | c.sendOpts()
	pkt := ipmsg.NewPacket(ipmsg.NextPacketNo(), c.nickname(), ipmsg.Hostname(), cmd,
		ipmsg.EncodeTextOut(text, c.cfg.GBK))
	c.udp.Broadcast(pkt)
	c.history.Append(systemSession, "我(广播)", text)
}

// sendTo 向单个好友发消息；不在线则入离线队列。
func (c *Core) sendTo(ip, text string) {
	if p := c.pals.Get(ip); p == nil || !p.Online {
		c.mu.Lock()
		cmd := ipmsg.CmdSendMsg | c.sendOpts()
		pkt := ipmsg.NewPacket(ipmsg.NextPacketNo(), c.nickname(), ipmsg.Hostname(), cmd,
			ipmsg.EncodeTextOut(text, c.cfg.GBK))
		c.offline[ip] = append(c.offline[ip], offlineMsg{ip: ip, data: ipmsg.EncodePacket(pkt)})
		c.mu.Unlock()
		c.history.Append(ip, "我", text)
		c.emit(NoticeEvent{Text: fmt.Sprintf("%s 当前不在线，消息将在其上线后自动发送", ip)})
		return
	}
	cmd := ipmsg.CmdSendMsg | ipmsg.OptSendCheck | c.sendOpts()
	pkt := ipmsg.NewPacket(ipmsg.NextPacketNo(), c.nickname(), ipmsg.Hostname(), cmd,
		ipmsg.EncodeTextOut(text, c.cfg.GBK))
	if err := c.udp.SendTo(ip, c.peerPort(ip), pkt); err != nil {
		c.emit(NoticeEvent{Text: "发送失败: " + err.Error()})
		return
	}
	c.history.Append(ip, "我", text)
}

// flushOffline 好友上线后补发离线消息。
func (c *Core) flushOffline(ip string) {
	c.mu.Lock()
	q := c.offline[ip]
	delete(c.offline, ip)
	c.mu.Unlock()
	for _, m := range q {
		_ = c.udp.Outbound(m.ip, c.peerPort(m.ip), m.data)
	}
	if len(q) > 0 {
		c.emit(NoticeEvent{Text: fmt.Sprintf("已向 %s 补发 %d 条离线消息", ip, len(q))})
	}
}

// SendFiles 向多个好友发送文件/文件夹（先发附件通告，等对方来拉）。
func (c *Core) SendFiles(ips []string, paths []string, note string) {
	if len(paths) == 0 {
		return
	}
	var files []ipmsg.FileEntry
	var localPaths []string
	for i, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			c.emit(NoticeEvent{Text: "无法读取 " + p + ": " + err.Error()})
			return
		}
		attr := ipmsg.FileRegular
		if st.IsDir() {
			attr = ipmsg.FileDir
		}
		files = append(files, ipmsg.FileEntry{
			ID:    uint32(i),
			Name:  filepath.Base(p),
			Size:  map[bool]int64{true: 0, false: st.Size()}[st.IsDir()],
			Mtime: st.ModTime().Unix(),
			Attr:  attr,
		})
		localPaths = append(localPaths, p)
	}
	packetNo := ipmsg.NextPacketNo()
	c.mu.Lock()
	peerSet := make(map[string]bool, len(ips))
	for _, ip := range ips {
		peerSet[ip] = true
	}
	c.sendSessions[packetNo] = &sendSession{packetNo: packetNo, files: files, paths: localPaths, peers: peerSet, label: note, createdAt: time.Now()}
	c.mu.Unlock()

	if note == "" {
		note = "[文件]"
	}
	extra := append(ipmsg.EncodeTextOut(note, c.cfg.GBK), 0)
	extra = append(extra, []byte(ipmsg.EncodeAttach(files, c.cfg.GBK))...)
	cmd := ipmsg.CmdSendMsg | ipmsg.OptSendCheck | ipmsg.OptFileAttach | c.sendOpts()
	for _, ip := range ips {
		if p := c.pals.Get(ip); p == nil || !p.Online {
			c.emit(NoticeEvent{Text: ip + " 不在线，文件通告未发送"})
			continue
		}
		pkt := ipmsg.NewPacket(packetNo, c.nickname(), ipmsg.Hostname(), cmd, extra)
		if err := c.udp.SendTo(ip, c.peerPort(ip), pkt); err != nil {
			c.emit(NoticeEvent{Text: "文件通告发送失败: " + err.Error()})
		}
	}
	names := ""
	for i, f := range files {
		if i > 0 {
			names += ", "
		}
		names += f.Name
	}
	c.emit(NoticeEvent{Text: fmt.Sprintf("已向 %d 位好友通告文件: %s（等待对方接收）", len(ips), names)})
}

// RejectFiles 拒绝对方的附件通告。
func (c *Core) RejectFiles(ip string, packetNo uint32) {
	c.mu.Lock()
	delete(c.recvOffers, packetNo)
	c.mu.Unlock()
	pkt := ipmsg.NewPacket(ipmsg.NextPacketNo(), c.nickname(), ipmsg.Hostname(),
		ipmsg.CmdReleaseFiles, []byte(fmt.Sprintf("%d", packetNo)))
	_ = c.udp.SendTo(ip, c.peerPort(ip), pkt)
}

// AcceptFiles 接收对方通告的全部文件，进度经 TransEvent 上报。
func (c *Core) AcceptFiles(packetNo uint32, saveDir string) {
	c.mu.Lock()
	offer, ok := c.recvOffers[packetNo]
	delete(c.recvOffers, packetNo)
	c.mu.Unlock()
	if !ok {
		return
	}
	go c.pullFiles(offer, saveDir)
}

func (c *Core) pullFiles(offer *recvOffer, saveDir string) {
	if saveDir == "" {
		saveDir = c.cfg.DownloadDir
	}
	_ = os.MkdirAll(saveDir, 0o755)
	task := fmt.Sprintf("接收 %s", offer.pal.Name)
	for _, f := range offer.files {
		if f.Attr == ipmsg.FileDir {
			c.emit(TransEvent{Task: task, Label: f.Name + "/", Done: 0, Total: 0, State: "progress"})
			// 目录流自带根目录名，dest 直接传保存父目录
			_, err := ipmsg.PullDir(offer.pal.IP, c.peerPort(offer.pal.IP), c.nickname(), ipmsg.Hostname(),
				offer.packetNo, f.ID, saveDir, func(done int64) {
					c.emit(TransEvent{Task: task, Label: f.Name + "/", Done: done, Total: -1, State: "progress"})
				})
			if err != nil {
				c.emit(TransEvent{Task: task, Label: f.Name, State: "error", Err: err.Error()})
				return
			}
			c.emit(TransEvent{Task: task, Label: f.Name + "/", State: "done", Done: 0, Total: -1})
		} else {
			dstPath := filepath.Join(saveDir, f.Name)
			dstPath = uniquePath(dstPath)
			c.emit(TransEvent{Task: task, Label: f.Name, Done: 0, Total: f.Size, State: "progress"})
			fw, err := os.Create(dstPath)
			if err != nil {
				c.emit(TransEvent{Task: task, Label: f.Name, State: "error", Err: err.Error()})
				return
			}
			n, err := ipmsg.PullFile(offer.pal.IP, c.peerPort(offer.pal.IP), c.nickname(), ipmsg.Hostname(),
				offer.packetNo, f.ID, 0, f.Size, fw,
				func(done int64) {
					c.emit(TransEvent{Task: task, Label: f.Name, Done: done, Total: f.Size, State: "progress"})
				})
			fw.Close()
			if err != nil || n != f.Size {
				msg := ""
				if err != nil {
					msg = err.Error()
				} else {
					msg = fmt.Sprintf("大小不符 %d/%d", n, f.Size)
				}
				c.emit(TransEvent{Task: task, Label: f.Name, State: "error", Err: msg})
				return
			}
			c.emit(TransEvent{Task: task, Label: f.Name, Done: f.Size, Total: f.Size, State: "done"})
		}
	}
}

// uniquePath 避免覆盖已存在文件：name.ext → name(1).ext。
func uniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := p[:len(p)-len(ext)]
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s(%d)%s", base, i, ext)
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
}
