package core

import (
	"net"
	"testing"
)

func netIP(a, b, c, d byte) net.IP { return net.IPv4(a, b, c, d) }

func TestProbeNetsForIPs(t *testing.T) {
	got := ProbeNetsForIPs([]string{
		"192.168.1.112",  // 普通局域网：不探测
		"100.106.206.67", // Tailscale CGNAT：/16
		"10.1.2.3",       // 私网：不探测
		"100.64.0.1",     // CGNAT 起点段
		"100.127.9.9",    // CGNAT 终点段
		"100.128.1.1",    // 超出 /10：不探测
		"bad-ip",
	})
	want := []string{"100.106.0.0/16", "100.64.0.0/16", "100.127.0.0/16"}
	if len(got) != len(want) {
		t.Fatalf("数量不符: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个不符: %s != %s", i, got[i], want[i])
		}
	}
}

func TestIncIP(t *testing.T) {
	ip := netIP(192, 168, 0, 255)
	incIP(ip)
	if ip.String() != "192.168.1.0" {
		t.Fatalf("进位错误: %s", ip.String())
	}
}
