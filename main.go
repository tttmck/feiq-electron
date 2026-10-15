// feiq-tui 是飞秋 (FeiQ/IPMSG) 的终端版客户端，运行于 macOS/Linux，
// 与 Windows 飞秋、飞鸽传书、iptux 在局域网内互通。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"feiq-tui/internal/core"
	"feiq-tui/internal/ui"
)

func main() {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".feiq-tui")

	var (
		nick    = flag.String("name", "", "昵称（默认 用户名@主机名）")
		group   = flag.String("group", "", "我的分组名")
		port    = flag.Int("port", 2425, "IPMSG 端口")
		gbk     = flag.Bool("gbk", false, "发送侧强制 GBK 编码（兼容极老客户端）")
		stealth = flag.Bool("stealth", false, "隐身启动（不上线广播）")
		dl      = flag.String("dir", "", fmt.Sprintf("接收文件保存目录（默认 %s）", filepath.Join(dataDir, "downloads")))
		serve   = flag.Bool("serve", false, "服务模式：无界面，stdin/stdout 走 JSON-RPC（供 Electron 等前端调用）")
	)
	flag.Parse()

	cfg := loadConfig(dataDir)
	if *nick != "" {
		cfg.Nickname = *nick
	}
	if *group != "" {
		cfg.Group = *group
	}
	cfg.Port = *port
	if *gbk {
		cfg.GBK = true
	}
	if *stealth {
		cfg.Stealth = true
	}
	if *dl != "" {
		cfg.DownloadDir = *dl
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = filepath.Join(dataDir, "downloads")
	}

	if *serve {
		if err := core.RunServe(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "服务模式错误:", err)
			os.Exit(1)
		}
		return
	}

	c := core.New(cfg)
	if err := c.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		fmt.Fprintln(os.Stderr, "（2425 端口被占用？可能是飞鸽/飞秋类程序已在运行）")
		os.Exit(1)
	}
	c.BroadcastEntry()

	app := ui.New(c)
	uiErr := app.Run()
	c.Shutdown() // 不用 defer：os.Exit 前必须广播下线
	if uiErr != nil {
		fmt.Fprintln(os.Stderr, "界面错误:", uiErr)
		os.Exit(1)
	}
}

// persistedCfg 是 ~/.feiq-tui/config.json 的持久化字段。
type persistedCfg struct {
	Nickname    string   `json:"nickname"`
	Group       string   `json:"group"`
	DownloadDir string   `json:"download_dir"`
	Blacklist   []string `json:"blacklist"`
	ProbeNets   []string `json:"probe_nets"`
}

// loadConfig 读取配置文件（不存在则给默认值），并回写以便用户修改。
func loadConfig(dataDir string) core.Config {
	_ = os.MkdirAll(dataDir, 0o755)
	path := filepath.Join(dataDir, "config.json")
	var p persistedCfg
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	if p.DownloadDir == "" {
		p.DownloadDir = filepath.Join(dataDir, "downloads")
	}
	if b, err := json.MarshalIndent(p, "", "  "); err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
	return core.Config{
		Nickname:    p.Nickname,
		Group:       p.Group,
		DownloadDir: p.DownloadDir,
		Blacklist:   p.Blacklist,
		ProbeNets:   p.ProbeNets,
		DataDir:     dataDir,
	}
}
