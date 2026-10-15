# feiq-tui

飞秋（FeiQ / IP Messenger）的终端版客户端，运行于 macOS / Linux，
与 Windows 上的**飞秋、飞鸽传书（IPMSG）、iptux** 在局域网内直接互通。

## 功能

- 好友自动发现（UDP 2425 广播，与飞秋同协议；支持分组显示）
- 点对点聊天、多人群发（列表空格勾选）、全网段广播
- 文件 / 文件夹传输（TCP 2425，飞秋同款协议，中文文件名自动 GBK 转换）
- 送达回执、离线消息（对方上线自动补发）、离开状态、隐身启动
- 聊天记录本地保存（`~/.feiq-tui/history/`）
- 收文件弹窗确认，自动保存到 `~/.feiq-tui/downloads/`（同名自动加 `(1)`）

## 两种界面

| 版本 | 目录 | 启动方式 |
|---|---|---|
| **Electron 图形版（App）** | `/Applications/飞秋.app` | 双击 / Spotlight 搜「飞秋」 / 命令 `feiq` |
| Electron 开发模式 | `electron/` | `cd electron && npm start` |
| 终端 TUI 版 | 项目根 | `./feiq-tui` |

### 打包 App

```bash
cd feiq-tui && go build -o feiq-tui .
cd electron && npm install
npx electron-builder --mac dir
# 产物在 electron/dist/mac-arm64/飞秋.app，拷入 /Applications 后:
codesign --force --deep --sign - /Applications/飞秋.app
```

Go 后端二进制会打进 App 的 Resources 里，App 独立运行，不依赖终端与 nvm。

### Electron 图形版

```bash
cd feiq-tui && go build -o feiq-tui .   # 先编译 Go 后端
cd electron && npm install && npm start # 首次需 install
```

架构：Electron 主进程拉起 `feiq-tui --serve` 子进程（JSON-RPC over stdio），
界面为原生 HTML/CSS/JS（无框架）。可用环境变量覆盖后端参数：
`FEIQ_NAME`（昵称）、`FEIQ_GROUP`（分组）、`FEIQ_PORT`（端口）、`FEIQ_DIR`（下载目录）。

图形版操作：左侧好友列表点选开聊（会话为顶部标签页，可同时开多个）；
勾选框多选即群发；「发送文件」走系统文件对话框（可多选文件与文件夹）；
收到文件弹原生确认框；传输进度点工具栏「传输」查看；失焦时新消息弹系统通知。
系统提示、广播与群发记录统一在常驻的「系统」标签页里。

### 终端 TUI 版

需要 Go 1.25+：

```bash
cd feiq-tui
go build -o feiq-tui .
./feiq-tui
```

首次运行生成 `~/.feiq-tui/config.json`，可改默认昵称 / 分组 / 下载目录 / 黑名单。

命令行参数（覆盖配置文件）：

```bash
./feiq-tui -name 张三 -group 技术部      # 昵称与分组
./feiq-tui -stealth                      # 隐身启动（不广播上线）
./feiq-tui -gbk                          # 强制 GBK 发送（极老客户端乱码时用）
./feiq-tui -dir ~/Downloads              # 接收文件保存目录
./feiq-tui -port 2425                    # 端口（默认 2425，与飞秋一致）
```

## 界面操作

```
┌─ 好友 ──────┬─ 聊天 ──────────────────────┐
│ ▾ 未分组 (2) │  ● 张三 上线                 │
│   张三 (PC)  │  张三: 你好                  │
│   李四 (MBP) │  我: 你好！                  │
│             ├─────────────────────────────┤
│             │ 输入> _                      │
├─────────────┴─────────────────────────────┤
│ F1帮助 F2刷新 F3广播 F4发文件 F8传输 F9离开 F10退出 │
└───────────────────────────────────────────┘
```

| 按键 | 作用 |
|---|---|
| Enter（列表） | 选中好友，右侧切换为其会话 |
| 空格（列表） | 勾选 / 取消群发对象 |
| Tab | 好友列表 ↔ 输入框切换焦点 |
| Enter（输入框） | 发送消息给当前对象（单选 / 群发 / 广播） |
| F2 | 重新广播上线（刷新好友） |
| F3 | 切换广播模式（输入框内容发到全网段） |
| F4 | 发送文件：输入本地路径（空格分隔多个） |
| `/file 路径…` | 同 F4，命令形式 |
| F8 | 传输进度面板 |
| F9 | 切换离开状态 |
| F10 | 退出（自动广播下线） |

收到文件时会弹出确认框：**Enter=接收，Esc=拒绝**。

## 与飞秋互通的协议说明

- 消息走 UDP 2425，格式 `1:包号:昵称:主机名:命令字:附加段`，
  命令字 = 功能（低 8 位）+ 选项（高 24 位）
- 发送侧默认 UTF-8 编码并带 `UTF8OPT (0x800000)` 标志；
  接收侧先按 UTF-8 校验、再按 GB18030 解码——飞秋中文消息默认 GBK，直接可读
- 文件传输：发送方先发 `SENDMSG|FILEATTACH` 通告
  （`文件ID:文件名:大小:时间:属性`，十六进制，文件名 `:`→`::` 转义），
  接收方确认后 TCP 连 2425 发 `GETFILEDATA`（`包号:文件ID:偏移`）拉取原始字节
- 文件夹走 `GETDIRFILES` 目录流：`头长:名字:大小:属性:14=mtime:16=ctime:` 自描述格式，
  深度优先、`RETPARENT` 项返回上级；发送侧文件名按 GBK 编码以兼容 Windows
- 上线/下线/状态用 `BR_ENTRY / ANSENTRY / BR_EXIT / BR_ABSENCE`

### 未覆盖的飞秋私有功能

飞秋的群聊、截图、图片消息、远程协助、共享目录是**私有协议扩展（无公开文档）**，
本客户端暂未实现（收到此类消息会忽略或显示占位文本）。
若需要精确兼容，可用逆向工具（如 Ghidra + REA）分析 `FeiQ.exe` 的私有命令字后补充。

## 开发

```bash
go test ./...   # 协议层单测 + 双实例集成测试（发现/聊天/传文件/离线补发）
```

目录结构：

```
internal/ipmsg   协议层：报文编解码、GBK 转换、UDP 广播、TCP 文件/目录流
internal/core    业务层：好友表、事件分发、传输会话、离线队列、聊天记录
internal/ui      终端界面：tview 三栏布局、确认弹窗、传输面板
main.go          入口与配置
```
