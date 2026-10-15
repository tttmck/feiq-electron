# feiq-electron

[中文说明](README.zh-CN.md)

A macOS client for **FeiQ (Fei Qiu) / IP Messenger**, built with an Electron UI and a Go backend.
It talks directly to Windows **FeiQ, IPMsg (飞鸽传书) and iptux** clients over the local network.

The interface replicates the classic **FeiQ 2013** look (skin assets extracted from the original
FeiQ.exe resources — XP aqua style).

## Features

- Automatic peer discovery (UDP 2425 broadcast, same protocol as FeiQ; grouped display;
  active probing for Tailscale subnets)
- One-to-one chat, multi-recipient group send (checkbox selection), subnet-wide broadcast
- File and folder transfer (TCP 2425, FeiQ-compatible protocol; Chinese filenames auto-converted to GBK)
- Delivery receipts, offline messages (auto-retransmitted when the peer comes online), away status
- Chat history persisted locally (`~/.feiq-tui/history/`)
- Incoming files require confirmation and are saved to `~/.feiq-tui/downloads/`
  (duplicates get `(1)` suffix automatically)

## Usage

The packaged app is self-contained (the Go backend binary is bundled inside the app bundle —
no Go or Node installation required):

- Double-click `/Applications/飞秋.app`, or find "飞秋" in Spotlight
- Or run `feiq` in a terminal (equivalent to `open -a 飞秋`)

Click a contact on the left to open a chat (multiple sessions live as tabs on top);
tick checkboxes to select multiple recipients for a group send; "Send File" opens the native
file dialog (multiple files and folders supported); incoming files show a native confirmation
dialog; transfer progress lives behind the "Transfers" toolbar button; new messages raise a
system notification while the window is unfocused. System notices, broadcasts and group-send
logs are collected in the always-present "System" tab.

## Architecture

The Electron main process spawns a `feiq-tui --serve` child process (JSON-RPC over stdio).
All protocol and business logic lives in the Go core; the UI is plain HTML/CSS/JS (no framework):

```
internal/ipmsg   Protocol layer: packet codec, GBK conversion, UDP broadcast, TCP file/dir streams
internal/core    Core logic: peer table, event dispatch, transfer sessions, offline queue,
                 chat history, serve mode
electron/        Electron shell: main-process bridge, preload IPC, renderer UI and skin
main.go          Entry point and config (--serve service mode)
```

Config file `~/.feiq-tui/config.json`: nickname / group / download dir / blacklist / probe nets.
Environment variables override backend parameters: `FEIQ_NAME`, `FEIQ_GROUP`, `FEIQ_PORT`, `FEIQ_DIR`.

## Development & Packaging

```bash
# Dev mode (build the Go backend first, then start Electron)
go build -o feiq-tui .
cd electron && npm install && npm start

# Package the app
cd electron && npx electron-builder --mac dir
# Output: electron/dist/mac-arm64/飞秋.app — copy into /Applications, then:
codesign --force --deep --sign - /Applications/飞秋.app

# Tests
go test ./...   # protocol unit tests + two-instance integration tests
                # (discovery / chat / file transfer / offline retransmit)
```

## Protocol notes (FeiQ interoperability)

- Messages travel over UDP 2425 in the form `1:packetNo:nickname:hostname:command:extra`,
  where command = function (low 8 bits) + options (upper 24 bits)
- The sending side uses UTF-8 with the `UTF8OPT (0x800000)` flag by default;
  the receiving side validates UTF-8 first, then falls back to GB18030 decoding —
  FeiQ's default GBK Chinese messages are readable out of the box
- File transfer: the sender first announces with `SENDMSG|FILEATTACH`
  (`fileID:name:size:mtime:attr`, hex-encoded, `:` in names escaped as `::`);
  after confirmation the receiver connects over TCP 2425 and pulls raw bytes
  with `GETFILEDATA` (`packetNo:fileID:offset`)
- Folders use the `GETDIRFILES` directory stream: a self-describing
  `headerLen:name:size:attr:14=mtime:16=ctime:` format, depth-first, with a `RETPARENT`
  entry to go up one level; the sender encodes filenames as GBK for Windows compatibility
- Presence uses `BR_ENTRY / ANSENTRY / BR_EXIT / BR_ABSENCE`

### FeiQ proprietary features not covered

FeiQ's group chat, screenshot, image messages, remote assistance and shared folders are
**proprietary protocol extensions without public documentation** and are not implemented
(such messages are ignored or shown as placeholder text). Exact compatibility would require
reverse engineering the private command codes in `FeiQ.exe` (e.g. with Ghidra + REA).
