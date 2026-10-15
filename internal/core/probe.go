package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"time"

	"feiq-tui/internal/ipmsg"
)

// probeIntervalLim 限制网段扫描频率：同网段 5 分钟内不重扫。
const probeIntervalLim = 5 * time.Minute

// ProbeNetsForIPs 从本机 IPv4 列表推断需要主动探测的网段：
// 100.64.0.0/10（CGNAT，Tailscale 等 mesh VPN）内的地址按 /16 归并。
// 点对点隧道没有广播，这些网段的好友只能靠主动单播发现。
func ProbeNetsForIPs(ips []string) []string {
	var nets []string
	seen := map[string]bool{}
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil || ip.To4() == nil {
			continue
		}
		b := ip.To4()
		if b[0] != 100 || b[1] < 64 || b[1] > 127 {
			continue
		}
		cidr := fmt.Sprintf("%d.%d.0.0/16", b[0], b[1])
		if !seen[cidr] {
			seen[cidr] = true
			nets = append(nets, cidr)
		}
	}
	return nets
}

// tailscalePeers 用 tailscale CLI 拿当前在线的 peer IPv4 列表。
// 无 CLI / 失败 / 无 peer 时返回 nil（调用方退回网段扫描）。
func tailscalePeers() []string {
	path, err := exec.LookPath("tailscale")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "status", "--json").Output()
	if err != nil {
		return nil
	}
	var st struct {
		Peer map[string]struct {
			Online      bool     `json:"Online"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return nil
	}
	var ips []string
	for _, p := range st.Peer {
		if !p.Online {
			continue
		}
		for _, ip := range p.TailscaleIPs {
			if v := net.ParseIP(ip); v != nil && v.To4() != nil {
				ips = append(ips, v.To4().String())
			}
		}
	}
	return ips
}

// probePeers 对无法靠广播发现的网段做主动探测：
// 1) 有 tailscale CLI：直接对在线 peer 单播上线通告（精确、轻量）
// 2) 否则：扫描自动推断 + 配置的网段，逐 IP 单播（5 分钟冷却）
func (c *Core) probePeers() {
	if ips := tailscalePeers(); len(ips) > 0 {
		c.logger.Info("tailscale 探测", "peers", len(ips))
		for _, ip := range ips {
			c.PingEntryTo(ip, c.cfg.Port)
		}
		return
	}

	nets := append(ProbeNetsForIPs(ipmsg.LocalIPv4s()), c.cfg.ProbeNets...)
	for _, n := range nets {
		if c.probeCooldown(n) {
			continue
		}
		c.scanNet(n)
	}
}

// probeCooldown 判断网段是否仍在冷却期。
func (c *Core) probeCooldown(net string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probedAt == nil {
		c.probedAt = make(map[string]time.Time)
	}
	if t, ok := c.probedAt[net]; ok && time.Since(t) < probeIntervalLim {
		return true
	}
	c.probedAt[net] = time.Now()
	return false
}

// scanNet 对网段内每个 IPv4 地址单播一份上线通告。
// UDP 无连接、包小（几十字节），256 并发扫 /16 也只需数秒；
// 对端在跑飞秋/飞鸽/feiq-tui 即会回 ANSENTRY，从而互相发现。
func (c *Core) scanNet(cidr string) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		c.logger.Warn("探测网段格式错误", "cidr", cidr)
		return
	}
	extra := ipmsg.EncodeTextOut(c.nickname(), c.cfg.GBK)
	if c.cfg.Group != "" {
		extra = append(extra, 0)
		extra = append(extra, ipmsg.EncodeTextOut(c.cfg.Group, c.cfg.GBK)...)
	}
	payload := ipmsg.EncodePacket(ipmsg.NewPacket(ipmsg.NextPacketNo(),
		c.nickname(), ipmsg.Hostname(), ipmsg.CmdBrEntry, extra))

	sem := make(chan struct{}, 256)
	var wg sync.WaitGroup
	for ip := ipnet.IP.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
		target := ip.String()
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			_ = c.udp.Outbound(target, c.cfg.Port, payload)
			<-sem
		}()
		// 拷贝下一地址：incIP 就地修改
	}
	wg.Wait()
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}
