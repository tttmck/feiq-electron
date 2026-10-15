// Package ui 实现 feiq-tui 的终端界面：飞秋风格的三栏布局。
package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"feiq-tui/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// App 持有全部界面状态。
type App struct {
	core  *core.Core
	app   *tview.Application
	pages *tview.Pages

	roster  *tview.TreeView // 好友列表
	chat    *tview.TextView // 聊天窗口
	input   *tview.InputField
	status  *tview.TextView
	transV  *tview.TextView // 传输面板内容
	mainLay *tview.Flex

	mu     sync.Mutex
	ip2node map[string]*tview.TreeNode
	node2ip map[*tview.TreeNode]string
	selIP   string            // 当前聊天对象
	selSet  map[string]bool   // 群发勾选（空格）
	sessions map[string]*session // 每好友会话缓冲

	offerQueue chan core.FileOfferEvent
	transLines []string // 传输面板最近记录
	broadcastMode bool
	absent       bool
}

type session struct {
	lines []string
}

// New 构建界面。
func New(c *core.Core) *App {
	a := &App{
		core:   c,
		app:    tview.NewApplication(),
		ip2node: make(map[string]*tview.TreeNode),
		node2ip: make(map[*tview.TreeNode]string),
		selSet:  make(map[string]bool),
		sessions: make(map[string]*session),
		offerQueue: make(chan core.FileOfferEvent, 256),
	}
	a.build()
	return a
}

// build 组装布局与事件绑定。
func (a *App) build() {
	a.roster = tview.NewTreeView().
		SetRoot(tview.NewTreeNode("好友").SetColor(tcell.ColorWhite)).
		SetCurrentNode(a.rosterRoot())
	a.roster.SetBorder(true).SetTitle(" 好友 ").SetTitleAlign(tview.AlignLeft)
	a.roster.SetSelectedFunc(a.onRosterSelect)
	a.roster.SetInputCapture(a.onRosterKey)

	a.chat = tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true)
	a.chat.SetBorder(true).SetTitle(" 聊天 ").SetTitleAlign(tview.AlignLeft)
	fmt.Fprintln(a.chat, "[gray]按 Enter 聚焦输入框，Tab 在列表/聊天/输入框间切换[-]")

	a.input = tview.NewInputField().
		SetLabel("输入> ").
		SetFieldBackgroundColor(tview.Styles.ContrastBackgroundColor)
	a.input.SetDoneFunc(a.onInputDone)

	a.status = tview.NewTextView().SetDynamicColors(true)
	a.status.SetText(a.statusText())

	right := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.chat, 0, 1, false).
		AddItem(a.input, 3, 1, true)

	a.mainLay = tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(a.roster, 32, 1, true).
		AddItem(right, 0, 4, false)

	rootFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.mainLay, 0, 1, true).
		AddItem(a.status, 1, 1, false)

	a.transV = tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	a.pages = tview.NewPages().AddPage("main", rootFlex, true, true)
	a.pages.AddPage("trans",
		tview.NewFlex().
			AddItem(nil, 0, 1, false).
			AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
				AddItem(nil, 0, 1, false).
				AddItem(center(a.transV, 60, 14), 16, 1, true).
				AddItem(nil, 0, 1, false), 64, 1, true).
			AddItem(nil, 0, 1, false),
		true, false)

	a.app.SetInputCapture(a.onGlobalKey)
	a.app.SetRoot(a.pages, true).EnableMouse(true)
	a.app.SetFocus(a.input)
}

func (a *App) rosterRoot() *tview.TreeNode { return a.roster.GetRoot() }

func center(p tview.Primitive, width, height int) tview.Primitive {
	return p
}

func (a *App) statusText() string {
	who := "（未选择好友）"
	if a.selIP != "" {
		if p := a.core.Pals().Get(a.selIP); p != nil {
			who = p.DisplayName()
		}
	}
	if a.broadcastMode {
		who = "广播（全网段）"
	} else if len(a.selSet) > 0 {
		who = fmt.Sprintf("群发（已选 %d 人）", len(a.selSet))
	}
	absTag := ""
	if a.absent {
		absTag = "[red](离开)[-] "
	}
	return fmt.Sprintf("%s[yellow]F1[-]帮助 [yellow]F2[-]刷新 [yellow]F3[-]广播 [yellow]F4[-]发文件 [yellow]F5[-]收文件确认 [yellow]F8[-]传输 [yellow]F9[-]离开 [yellow]F10[-]退出    对象: %s", absTag, who)
}

// Run 启动事件泵与主循环（阻塞）。
func (a *App) Run() error {
	go a.pump()
	go a.offerPump()
	return a.app.Run()
}

// pump 消费 core 事件并刷新界面。
// FileOfferEvent 在本 goroutine 直接入队（不经过 UI 闭包），
// 避免弹窗未确认积压时 QueueUpdateDraw 与 offerQueue 互等死锁。
func (a *App) pump() {
	for ev := range a.core.Events() {
		if off, ok := ev.(core.FileOfferEvent); ok {
			a.offerQueue <- off
			continue
		}
		a.app.QueueUpdateDraw(func() { a.handle(ev) })
	}
}

// offerPump 逐个弹窗确认收文件。
func (a *App) offerPump() {
	for off := range a.offerQueue {
		a.confirmOffer(off)
	}
}

// handle 处理一条 core 事件（在 UI 线程）。
func (a *App) handle(ev any) {
	switch e := ev.(type) {
	case core.PalEvent:
		a.refreshRoster()
		if e.Off {
			a.appendLine(e.Pal.IP, fmt.Sprintf("[gray]● %s 已下线[-]", e.Pal.Name))
		} else {
			a.appendLine(e.Pal.IP, fmt.Sprintf("[gray]● %s 上线[-]", e.Pal.DisplayName()))
		}
	case core.MsgInEvent:
		tag := ""
		if e.Broadcast {
			tag = "[广播]"
		}
		a.appendMsg(e.Pal.IP, e.Pal.Name, tag+e.Text, false)
		a.core.Pals().IncUnread(e.Pal.IP)
		if a.selIP != e.Pal.IP {
			a.beep()
		}
		a.refreshRoster()
	case core.AckEvent:
		a.appendLine(e.IP, "[gray]✓ 对方已收到[-]")
	case core.FileOfferEvent:
		// 已在 pump 中直通 offerQueue，见 pump 注释
	case core.TransEvent:
		a.pushTrans(e)
	case core.NoticeEvent:
		if a.selIP != "" {
			a.appendLine(a.selIP, "[gray]· "+e.Text+"[-]")
		} else {
			a.pushNotice(e.Text)
		}
	}
}

// ---- 好友列表 ----

// beep 通过 /dev/tty 发终端响铃（不干扰 tview 的屏幕缓冲）。
func (a *App) beep() {
	if f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		_, _ = f.WriteString("\a")
		f.Close()
	}
}

// refreshRoster 按好友表重建列表，尽量保持当前光标位置。
func (a *App) refreshRoster() {
	a.mu.Lock()
	defer a.mu.Unlock()
	pals := a.core.Pals().All()
	groups := map[string][]*core.Pal{}
	for _, p := range pals {
		g := p.Group
		if g == "" {
			g = "未分组"
		}
		groups[g] = append(groups[g], p)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)

	root := a.roster.GetRoot()
	root.ClearChildren()
	a.ip2node = make(map[string]*tview.TreeNode)
	a.node2ip = make(map[*tview.TreeNode]string)

	for _, g := range names {
		gn := tview.NewTreeNode(fmt.Sprintf("▾ %s (%d)", g, len(groups[g]))).SetColor(tcell.ColorYellow).SetSelectable(true)
		for _, p := range groups[g] {
			label := "  " + p.DisplayName()
			if p.Unread > 0 {
				label = fmt.Sprintf("  %s [red](%d)[-]", strings.TrimSpace(label), p.Unread)
			}
			if a.selSet[p.IP] {
				label = "√ " + strings.TrimSpace(label)
			}
			if !p.Online {
				label = "  " + p.DisplayName() + " [gray](离线)[-]"
			} else if p.Absence {
				label = "  " + p.DisplayName() + " [teal](离开)[-]"
			}
			n := tview.NewTreeNode(label).SetSelectable(true).SetReference(p.IP)
			a.ip2node[p.IP] = n
			a.node2ip[n] = p.IP
			gn.AddChild(n)
		}
		root.AddChild(gn)
	}
	root.SetExpanded(true)
	// 恢复光标：仍在列表中的好友，否则首个好友，否则根
	if cur := a.roster.GetCurrentNode(); cur != nil && cur.GetReference() != nil {
		if n := a.ip2node[cur.GetReference().(string)]; n != nil {
			a.roster.SetCurrentNode(n)
			a.status.SetText(a.statusText())
			return
		}
	}
	a.roster.SetCurrentNode(root)
	for _, n := range a.ip2node {
		a.roster.SetCurrentNode(n)
		break
	}
	a.status.SetText(a.statusText())
}

// onRosterSelect 选中好友：切换聊天会话。
func (a *App) onRosterSelect(n *tview.TreeNode) {
	if n.GetReference() == nil {
		n.SetExpanded(!n.IsExpanded())
		return
	}
	ip := n.GetReference().(string)
	a.selIP = ip
	a.core.Pals().ClearUnread(ip)
	a.showSession(ip)
	a.refreshRoster()
}

// onRosterKey 好友列表按键：空格勾选群发。
func (a *App) onRosterKey(ev *tcell.EventKey) *tcell.EventKey {
	if ev.Key() == tcell.KeyRune && ev.Rune() == ' ' {
		if n := a.roster.GetCurrentNode(); n != nil && n.GetReference() != nil {
			ip := n.GetReference().(string)
			if a.selSet[ip] {
				delete(a.selSet, ip)
			} else {
				a.selSet[ip] = true
			}
			a.refreshRoster()
			return nil
		}
	}
	return ev
}

// ---- 聊天 ----

// appendMsg 追加一条消息到会话（落盘由 core 统一处理）。
func (a *App) appendMsg(ip, who, text string, mine bool) {
	prefix := who + ": "
	if mine {
		prefix = "我: "
	}
	a.appendLine(ip, prefix+text)
}

// appendLine 追加一行显示（当前会话才直接刷新）。
func (a *App) appendLine(ip, line string) {
	a.mu.Lock()
	s, ok := a.sessions[ip]
	if !ok {
		s = &session{}
		a.sessions[ip] = s
	}
	s.lines = append(s.lines, line)
	if len(s.lines) > 500 {
		s.lines = s.lines[len(s.lines)-500:]
	}
	a.mu.Unlock()
	if ip == a.selIP {
		fmt.Fprintln(a.chat, line)
		a.chat.ScrollToEnd()
	}
}

// showSession 切换到某好友会话视图。
func (a *App) showSession(ip string) {
	a.chat.Clear()
	a.mu.Lock()
	s := a.sessions[ip]
	a.mu.Unlock()
	a.chat.SetTitle(fmt.Sprintf(" 聊天: %s ", ip))
	if s != nil {
		for _, l := range s.lines {
			fmt.Fprintln(a.chat, l)
		}
	} else {
		for _, l := range a.core.HistoryLoad(ip, 50) {
			fmt.Fprintln(a.chat, "[gray]"+l+"[-]")
		}
	}
	a.chat.ScrollToEnd()
}

// onInputDone 处理输入框回车。
func (a *App) onInputDone(key tcell.Key) {
	if key != tcell.KeyEnter {
		return
	}
	text := strings.TrimSpace(a.input.GetText())
	a.input.SetText("")
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/") {
		a.command(text)
		return
	}
	switch {
	case a.broadcastMode:
		a.core.BroadcastText(text)
		a.appendCur(fmt.Sprintf("我(广播): %s", text))
	case len(a.selSet) > 0:
		ips := make([]string, 0, len(a.selSet))
		for ip := range a.selSet {
			ips = append(ips, ip)
		}
		a.core.SendMessage(ips, text)
		a.appendCur(fmt.Sprintf("我(群发): %s", text))
	case a.selIP != "":
		a.core.SendMessage([]string{a.selIP}, text)
		a.appendCur("我: " + text)
	default:
		a.appendCur("[red]请先在左侧选择好友[-]")
	}
}

// appendCur 追加到当前会话。
func (a *App) appendCur(line string) {
	if a.selIP == "" {
		a.selIP = "_sys"
	}
	a.appendLine(a.selIP, line)
}

// command 处理斜杠命令。
func (a *App) command(cmd string) {
	fields := strings.Fields(cmd)
	switch fields[0] {
	case "/file":
		if len(fields) < 2 {
			a.appendCur("[red]用法: /file 路径1 路径2 ...[-]")
			return
		}
		a.sendFiles(fields[1:])
	case "/help":
		a.appendCur("[gray]命令: /file <路径...> 发送文件  /clear 清空当前会话[-]")
	case "/clear":
		a.mu.Lock()
		delete(a.sessions, a.selIP)
		a.mu.Unlock()
		a.showSession(a.selIP)
	default:
		a.appendCur("[red]未知命令 " + fields[0] + "[-]")
	}
}

// sendFiles 向当前目标发送文件。
func (a *App) sendFiles(paths []string) {
	if len(a.selSet) > 0 {
		ips := make([]string, 0, len(a.selSet))
		for ip := range a.selSet {
			ips = append(ips, ip)
		}
		a.core.SendFiles(ips, paths, "")
		return
	}
	if a.selIP == "" || a.selIP == "_sys" {
		a.appendCur("[red]请先选择好友[-]")
		return
	}
	a.core.SendFiles([]string{a.selIP}, paths, "")
}

// ---- 文件接收确认 ----

// confirmOffer 弹窗询问是否接收（阻塞 offerPump，形成队列效果）。
func (a *App) confirmOffer(e core.FileOfferEvent) {
	done := make(chan bool, 1)
	a.app.QueueUpdateDraw(func() {
		a.appendLine(e.Pal.IP, fmt.Sprintf("[gray]◎ 对方发送文件: %s[-]", offerSummary(e)))
		modal := tview.NewModal().
			SetText(fmt.Sprintf("%s 发送文件:\n%s\n\n保存到 %s\nEnter 接收 / Esc 拒绝",
				e.Pal.DisplayName(), offerSummary(e), a.core.Cfg().DownloadDir)).
			AddButtons([]string{"接收", "拒绝"}).
			SetDoneFunc(func(idx int, label string) {
				a.pages.RemovePage("offer")
				done <- idx == 0
			})
		a.pages.AddAndSwitchToPage("offer", modal, true)
	})
	accept := <-done
	if accept {
		a.core.AcceptFiles(e.PacketNo, "")
	} else {
		a.core.RejectFiles(e.Pal.IP, e.PacketNo)
		a.app.QueueUpdateDraw(func() {
			a.appendLine(e.Pal.IP, "[gray]已拒绝接收[-]")
			a.pages.SwitchToPage("main")
		})
		return
	}
	a.app.QueueUpdateDraw(func() { a.pages.SwitchToPage("main") })
}

func offerSummary(e core.FileOfferEvent) string {
	names := make([]string, 0, len(e.Files))
	total := int64(0)
	for _, f := range e.Files {
		names = append(names, f.Name)
		total += f.Size
	}
	s := strings.Join(names, ", ")
	if rs := []rune(s); len(rs) > 60 {
		s = string(rs[:60]) + "…"
	}
	return fmt.Sprintf("%s (共 %s)", s, humanSize(total))
}

// ---- 传输面板 ----

func (a *App) pushTrans(e core.TransEvent) {
	var line, plain string
	switch e.State {
	case "progress":
		if e.Total > 0 {
			line = fmt.Sprintf("[yellow]%s %s  %s/%s", e.Task, e.Label, humanSize(e.Done), humanSize(e.Total))
			plain = fmt.Sprintf("%s %s  %s/%s", e.Task, e.Label, humanSize(e.Done), humanSize(e.Total))
		} else {
			line = fmt.Sprintf("[yellow]%s %s  %s", e.Task, e.Label, humanSize(e.Done))
			plain = fmt.Sprintf("%s %s  %s", e.Task, e.Label, humanSize(e.Done))
		}
	case "done":
		line = fmt.Sprintf("[green]✓ %s %s 完成", e.Task, e.Label)
		plain = fmt.Sprintf("✓ %s %s 完成", e.Task, e.Label)
	case "error":
		line = fmt.Sprintf("[red]✗ %s %s 失败: %s", e.Task, e.Label, e.Err)
		plain = fmt.Sprintf("✗ %s %s 失败: %s", e.Task, e.Label, e.Err)
	}
	a.mu.Lock()
	a.transLines = append(a.transLines, line)
	if len(a.transLines) > 100 {
		a.transLines = a.transLines[len(a.transLines)-100:]
	}
	a.mu.Unlock()
	fmt.Fprintln(a.transV, line)
	a.transV.ScrollToEnd()
	a.appendCur("[gray]" + plain + "[-]")
}

func (a *App) pushNotice(text string) {
	a.appendCur("[gray]· " + text + "[-]")
}

// ---- 全局按键 ----

func (a *App) onGlobalKey(ev *tcell.EventKey) *tcell.EventKey {
	if a.pages.HasPage("offer") {
		return ev // 弹窗自行处理
	}
	switch ev.Key() {
	case tcell.KeyF1:
		a.appendCur("[gray]帮助: Tab 切焦点 | 空格(列表)勾选群发 | Enter 发送 | /file 路径 发文件 | F2 刷新 F3 广播 F4 文件 F5 处理收文件 F8 传输 F9 离开 F10 退出[-]")
		return nil
	case tcell.KeyF2:
		a.core.BroadcastEntry()
		a.appendCur("[gray]已重新广播上线，等待好友应答…[-]")
		return nil
	case tcell.KeyF3:
		a.broadcastMode = !a.broadcastMode
		a.status.SetText(a.statusText())
		return nil
	case tcell.KeyF4:
		a.promptPath()
		return nil
	case tcell.KeyF5:
		// 弹窗队列由 offerPump 自动逐个弹出；这里只提示积压数量
		a.appendCur(fmt.Sprintf("[gray]待确认文件 %d 个（弹窗将依次出现）[-]", len(a.offerQueue)))
		return nil
	case tcell.KeyF9:
		a.absent = !a.absent
		a.core.SetAbsence(a.absent, "离开")
		if a.absent {
			a.appendCur("[gray]已切换为离开状态[-]")
		} else {
			a.appendCur("[gray]已恢复在线[-]")
		}
		return nil
	case tcell.KeyF8:
		if a.pages.HasPage("trans") {
			a.pages.HidePage("trans")
		} else {
			a.pages.ShowPage("trans")
		}
		return nil
	case tcell.KeyF10:
		a.app.Stop()
		return nil
	case tcell.KeyTAB:
		// 列表 → 输入框 → 列表
		if a.roster.HasFocus() {
			a.app.SetFocus(a.input)
		} else {
			a.app.SetFocus(a.roster)
		}
		return nil
	}
	return ev
}

// promptPath 弹出文件路径输入框。
func (a *App) promptPath() {
	in := tview.NewInputField().SetLabel("文件路径(空格分隔多个): ")
	in.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			fields := strings.Fields(in.GetText())
			if len(fields) > 0 {
				a.sendFiles(fields)
			}
		}
		a.pages.RemovePage("path")
		a.app.SetFocus(a.input)
	})
	a.pages.AddAndSwitchToPage("path",
		tview.NewFlex().AddItem(nil, 0, 1, false).
			AddItem(in, 3, 1, true).
			AddItem(nil, 0, 1, false), true)
	a.app.SetFocus(in)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
