// feiq-desktop 主进程：
// 1. 拉起 ../feiq-tui --serve 子进程（JSON 行协议 over stdio）
// 2. 把渲染进程的 RPC 请求转发给子进程，把子进程的事件推给渲染进程
// 3. 提供原生文件对话框与系统通知
const { app, BrowserWindow, ipcMain, dialog, Notification } = require('electron');
const { spawn } = require('child_process');
const path = require('path');
const readline = require('readline');
const fs = require('fs');

const GO_BIN = app.isPackaged
  ? path.join(process.resourcesPath, 'feiq-tui')
  : path.join(__dirname, '..', 'feiq-tui');
let win = null;
let backend = null;
let nextId = 1;
const pending = new Map(); // id -> resolve

function log(...args) {
  console.log('[feiq-desktop]', ...args);
}

function startBackend() {
  const args = ['--serve'];
  if (process.env.FEIQ_NAME) args.push('-name', process.env.FEIQ_NAME);
  if (process.env.FEIQ_GROUP) args.push('-group', process.env.FEIQ_GROUP);
  if (process.env.FEIQ_PORT) args.push('-port', process.env.FEIQ_PORT);
  if (process.env.FEIQ_DIR) args.push('-dir', process.env.FEIQ_DIR);
  backend = spawn(GO_BIN, args, { stdio: ['pipe', 'pipe', 'inherit'] });
  // 后端死后写入 stdin 会触发 EPIPE：吞掉，由 exit 分支统一处理
  backend.stdin.on('error', () => {});
  const rl = readline.createInterface({ input: backend.stdout });
  rl.on('line', (line) => {
    line = line.trim();
    if (!line) return;
    let msg;
    try { msg = JSON.parse(line); } catch { log('无法解析后端行:', line); return; }
    if ('id' in msg) {
      const resolve = pending.get(msg.id);
      if (resolve) { pending.delete(msg.id); resolve(msg); }
    } else if (msg.event) {
      if (win && !win.isDestroyed()) win.webContents.send('backend-event', msg.event, msg.data);
      maybeNotify(msg.event, msg.data);
    }
  });
  backend.on('exit', (code) => {
    log('后端退出, code =', code);
    if (win && !win.isDestroyed()) {
      win.webContents.send('backend-event', 'backend-dead', { code });
    }
  });
}

// 窗口不在焦点时，消息/文件通告弹系统通知（60 秒内最多 5 条，防风暴）
let notifyCount = 0;
let notifyWindowStart = Date.now();
function notifyAllowed() {
  const now = Date.now();
  if (now - notifyWindowStart > 60000) {
    notifyWindowStart = now;
    notifyCount = 0;
  }
  return ++notifyCount <= 5;
}
function maybeNotify(event, data) {
  if (!win || win.isFocused() || !Notification.isSupported() || !notifyAllowed()) return;
  if (event === 'msg') {
    const pal = data && data.Pal ? data.Pal : {};
    new Notification({ title: `${pal.Name || '新消息'} (飞秋)`, body: String(data.Text || '') }).show();
  } else if (event === 'offer') {
    const pal = data && data.Pal ? data.Pal : {};
    new Notification({ title: `${pal.Name || '好友'} 发来文件`, body: '点击窗口查看并确认接收' }).show();
  }
}

// 渲染进程 RPC 入口
ipcMain.handle('rpc', async (_ev, method, params) => {
  if (!backend || backend.exitCode !== null) {
    return { error: '后端服务已退出，请重启应用' };
  }
  const id = nextId++;
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      pending.delete(id);
      resolve({ id, error: `后端响应超时: ${method}` });
    }, 10000);
    pending.set(id, (msg) => { clearTimeout(timer); resolve(msg); });
    backend.stdin.write(JSON.stringify({ id, method, params: params || {} }) + '\n');
  });
});

// 原生文件选择（发送文件用）
ipcMain.handle('pick-files', async () => {
  const r = await dialog.showOpenDialog(win, {
    title: '选择要发送的文件或文件夹',
    properties: ['openFile', 'openDirectory', 'multiSelections'],
  });
  return r.canceled ? [] : r.filePaths;
});

// 接收文件确认弹窗
ipcMain.handle('confirm-offer', async (_ev, summary) => {
  const r = await dialog.showMessageBox(win, {
    type: 'question',
    title: '接收文件',
    message: '对方发送文件，是否接收？',
    detail: summary,
    buttons: ['接收', '拒绝'],
    defaultId: 0,
    cancelId: 1,
  });
  return r.response === 0;
});

// 检查后端二进制存在
if (!fs.existsSync(GO_BIN)) {
  console.error('找不到后端二进制:', GO_BIN, '\n请先在项目根目录执行: go build -o feiq-tui .');
  app.exit(1);
}

function createWindow() {
  win = new BrowserWindow({
    width: 960,
    height: 640,
    minWidth: 720,
    minHeight: 480,
    title: '飞秋',
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
    },
  });
  win.setMenuBarVisibility(false);
  win.loadFile(path.join(__dirname, 'renderer', 'index.html'));
}

app.whenReady().then(() => {
  startBackend();
  createWindow();
  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});

app.on('before-quit', (ev) => {
  if (!backend || backend.exitCode !== null) return;
  if (backend.__graceful) return;
  ev.preventDefault();
  backend.__graceful = true;
  const forceTimer = setTimeout(() => backend.kill('SIGKILL'), 2000);
  backend.once('exit', () => { clearTimeout(forceTimer); app.quit(); });
  try { backend.stdin.end(); } catch {} // EOF 触发 Go 侧广播下线后退出
});
