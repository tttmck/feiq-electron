package core

import (
	"os"
	"testing"
)

// TestTailscalePeersLive 依赖本机 tailscale CLI（无则跳过）。
func TestTailscalePeersLive(t *testing.T) {
	if _, err := os.Stat("/usr/local/bin/tailscale"); err != nil {
		t.Skip("无 tailscale CLI")
	}
	ips := tailscalePeers()
	t.Logf("解析到在线 peers: %v", ips)
	if len(ips) == 0 {
		t.Log("在线 peer 为 0（解析逻辑仍需人工确认 JSON 结构）")
	}
}
