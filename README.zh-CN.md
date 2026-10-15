# feiq-electron

[English](README.md)

飞秋（FeiQ / IP Messenger）的 macOS 客户端，Electron 图形界面 + Go 后端，
与 Windows 上的**飞秋、飞鸽传书（IPMSG）、iptux** 在局域网内直接互通。

界面照飞秋 2013 原版风格复刻（皮肤素材取自 FeiQ.exe 解包位图，XP 水晶风）。

## 功能

- 好友自动发现（UDP 2425 广播，与飞秋同协议；分组显示；Tailscale 网段主动探测）
- 点对点聊天、多人群发（勾选）、全网段广播
- 文件 / 文件夹传输（TCP 2425，飞秋同款协议，中文文件名自动 GBK 转换）
- 送达回执、离线消息（对方上线自动补发）、离开状态
- 聊天记录本地保存（`~/.feiq-tui/history/`）
- 收文件弹窗确认，自动保存到 `~/.feiq-tui/downloads/`（同名自动加 `(1)`）

## 使用

打包好的 App 双击即用（Go 后端二进制已内嵌进 App，无需安装 Go / Node）：

- 双击 `/Applications/飞秋.app`，或 Spotlight 搜「飞秋」
- 终端命令 `feiq`（等价 `open -a 飞秋`）

操作：左侧好友列表点选开聊（顶部标签页可同时开多个）；
勾选框多选即群发；「发送文件」走系统文件对话框（可多选文件与文件夹）；
收到文件弹原生确认框；传输进度点工具栏「传输」查看；失焦时新消息弹系统通知。
系统提示、广播与群发记录统一在常驻的「系统」标签页里。

## 架构

Electron 主进程拉起 `feiq-tui --serve` 子进程（JSON-RPC over stdio），
协议与业务全在 Go 内核，界面为原生 HTML/CSS/JS（无框架）：

```
internal/ipmsg   协议层：报文编解码、GBK 转换、UDP 广播、TCP 文件/目录流
internal/core    业务层：好友表、事件分发、传输会话、离线队列、聊天记录、serve 模式
electron/        Electron 壳：主进程桥接、preload IPC、渲染层界面与皮肤
main.go          入口与配置（--serve 服务模式）
```

配置文件 `~/.feiq-tui/config.json`：昵称 / 分组 / 下载目录 / 黑名单 / 探测网段。
环境变量可覆盖后端参数：`FEIQ_NAME`、`FEIQ_GROUP`、`FEIQ_PORT`、`FEIQ_DIR`。

## 开发与打包

```bash
# 开发模式（先编译 Go 后端，再起 Electron）
go build -o feiq-tui .
cd electron && npm install && npm start

# 打包 App
cd electron && npx electron-builder --mac dir
# 产物在 electron/dist/mac-arm64/飞秋.app，拷入 /Applications 后:
codesign --force --deep --sign - /Applications/飞秋.app

# 测试
go test ./...   # 协议层单测 + 双实例集成测试（发现/聊天/传文件/离线补发）
```

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
