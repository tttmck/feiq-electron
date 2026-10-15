// feiq-desktop 渲染层逻辑
// 说明：Go 后端事件/数据结构序列化后字段名为导出名（如 Pal.Name、
// MsgInEvent.Text、TransEvent.Done），SelfInfo 例外（有 json tag）。

const $ = (sel) => document.querySelector(sel);
const SYS = '_sys'; // 常驻「系统」会话：通知/广播/群发记录都归这里

// 预览模式：不经 Electron 直接用浏览器打开页面时（无 window.feiq 桥），
// 先注入示例数据再执行后续顶层代码；正式运行 preload 一定会先定义 window.feiq。
// 必须放在文件开头：顶层就调用了 window.feiq.onEvent，晚了会把整个脚本打断。
if (!window.feiq) installPreviewMock();

const state = {
  self: null,
  pals: new Map(),      // ip -> pal（含 Unread/Group/Online/Absence）
  sessions: new Map(),  // ip -> { lines: [{cls, who, text}] }
  tabs: [],             // 打开的会话 ip 列表
  activeIP: '',
  tabNames: new Map(), // ip -> 标签名快照（好友下线后 tab 不变「未知」）
  selected: new Set(),  // 群发勾选
  broadcastMode: false,
  away: false,
  collapsed: new Set(), // 折叠的分组名
  trans: new Map(),     // task+label -> {task,label,done,total,state,err}
};

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

function humanSize(n) {
  if (n < 0) return '';
  if (n < 1024) return n + 'B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let u = -1;
  do { n /= 1024; u++; } while (n >= 1024 && u < units.length - 1);
  return n.toFixed(1) + units[u];
}

function palName(p) { return p && p.Name ? p.Name : '(未知)'; }

// ---- RPC 封装 ----
async function rpc(method, params) {
  const r = await window.feiq.rpc(method, params);
  if (r && r.error) throw new Error(r.error);
  return r ? r.result : null;
}

// ---- 好友列表 ----
function palGroups() {
  const groups = new Map();
  for (const p of state.pals.values()) {
    const g = p.Group || '未分组';
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push(p);
  }
  return [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0], 'zh'));
}

function renderRoster() {
  const tree = $('#pal-tree');
  $('#roster-empty').style.display = state.pals.size ? 'none' : 'block';
  const html = [];
  for (const [g, list] of palGroups()) {
    const collapsed = state.collapsed.has(g);
    html.push(`<div class="group-head" data-group="${esc(g)}">
      <span class="arrow">${collapsed ? '▸' : '▾'}</span>${esc(g)}
      <span class="count">(${list.length})</span></div>`);
    if (!collapsed) {
      for (const p of list) {
        const tags = [
          p.Absence ? '<span class="tag">离开</span>' : '',
          !p.Online ? '<span class="tag">离线</span>' : '',
        ].join('');
        const sub = p.Host && p.Host !== p.Name ? p.Host : p.IP;
        html.push(`<div class="pal-row ${p.IP === state.activeIP ? 'selected' : ''} ${p.Online ? '' : 'offline'}" data-ip="${esc(p.IP)}">
          <input type="checkbox" data-check="${esc(p.IP)}" ${state.selected.has(p.IP) ? 'checked' : ''}>
          <span class="avatar">${esc(palName(p).slice(0, 1))}</span>
          <span class="pal-main">
            <span class="line1"><span class="name">${esc(palName(p))}</span>${tags}${p.Unread > 0 ? `<span class="badge">${p.Unread}</span>` : ''}</span>
            <span class="line2">${esc(sub)}</span>
          </span>
        </div>`);
      }
    }
  }
  tree.innerHTML = html.join('');
}

$('#pal-tree').addEventListener('click', (ev) => {
  const groupHead = ev.target.closest('.group-head');
  if (groupHead) {
    const g = groupHead.dataset.group;
    state.collapsed.has(g) ? state.collapsed.delete(g) : state.collapsed.add(g);
    renderRoster();
    return;
  }
  const cb = ev.target.closest('input[type="checkbox"]');
  if (cb) {
    const ip = cb.dataset.check;
    state.selected.has(ip) ? state.selected.delete(ip) : state.selected.add(ip);
    renderTargetHint();
    renderRoster();
    return;
  }
  const row = ev.target.closest('.pal-row');
  if (row) openSession(row.dataset.ip);
});

// ---- 会话 ----
function sessionOf(ip) {
  if (!state.sessions.has(ip)) state.sessions.set(ip, { lines: [] });
  return state.sessions.get(ip);
}

function openSession(ip) {
  if (!state.tabs.includes(ip)) state.tabs.push(ip);
  state.activeIP = ip;
  const p = state.pals.get(ip);
  if (p) { p.Unread = 0; rpc('get_state').then(syncState).catch(() => {}); }
  renderAll();
}

function closeSession(ip) {
  state.tabs = state.tabs.filter((x) => x !== ip);
  if (state.activeIP === ip) state.activeIP = state.tabs[state.tabs.length - 1] || '';
  renderAll();
}

function appendLine(ip, line) {
  const s = sessionOf(ip);
  s.lines.push(line);
  if (s.lines.length > 800) s.lines.splice(0, s.lines.length - 800);
  if (ip === state.activeIP) appendLineDom(line);
}

function appendLineDom(line) {
  const log = $('#chat-log');
  const div = document.createElement('div');
  div.className = 'msg ' + (line.cls || '');
  if (line.cls === 'sys') {
    div.textContent = line.text;
  } else {
    const who = document.createElement('span');
    who.className = 'who' + (line.mine ? ' me' : '');
    who.textContent = line.who + ': ';
    const body = document.createElement('span');
    body.className = 'body';
    body.textContent = line.text;
    div.append(who, body);
  }
  log.appendChild(div);
  log.scrollTop = log.scrollHeight;
}

let chatReqToken = 0;
function renderChat() {
  const empty = $('#chat-empty');
  const log = $('#chat-log');
  if (!state.activeIP) {
    empty.style.display = 'flex';
    log.style.display = 'none';
    return;
  }
  empty.style.display = 'none';
  log.style.display = 'block';
  log.innerHTML = '';
  const ip = state.activeIP;
  const token = ++chatReqToken;
  // 历史记录（磁盘）+ 本次会话缓冲；响应到达时若已切走则丢弃
  rpc('load_history', { ip, lines: 50 }).then((r) => {
    if (token !== chatReqToken || ip !== state.activeIP) return;
    for (const l of (r && r.lines) || []) {
      appendLineDom({ cls: 'sys', text: l });
    }
    for (const line of sessionOf(ip).lines) appendLineDom(line);
  }).catch(() => {});
}

function renderTabs() {
  const tabs = $('#tabs');
  tabs.innerHTML = state.tabs.map((ip) => {
    const p = state.pals.get(ip);
    const name = ip === SYS ? '系统' : (state.tabNames.get(ip) || palName(p));
    const unread = ip === SYS ? 0 : (p && p.Unread > 0 ? p.Unread : 0);
    if (p) state.tabNames.set(ip, palName(p));
    return `<div class="tab ${ip === state.activeIP ? 'active' : ''}" data-ip="${esc(ip)}">
      <span>${esc(name)}</span>
      ${unread ? `<span class="unread">${unread}</span>` : ''}
      <span class="close" data-close="${esc(ip)}">×</span>
    </div>`;
  }).join('');
}

$('#tabs').addEventListener('click', (ev) => {
  const close = ev.target.closest('[data-close]');
  if (close) { closeSession(close.dataset.close); return; }
  const tab = ev.target.closest('.tab');
  if (tab) { state.activeIP = tab.dataset.ip; renderAll(); }
});

// ---- 输入与发送 ----
function renderTargetHint() {
  const hint = $('#target-hint');
  if (state.broadcastMode) {
    hint.textContent = '广播模式：消息将发送到全网段所有 IPMSG 客户端';
    return;
  }
  if (state.selected.size > 0) {
    const names = [...state.selected].map((ip) => palName(state.pals.get(ip))).join('、');
    hint.textContent = `群发（${state.selected.size} 人）：${names}`;
    return;
  }
  const p = state.pals.get(state.activeIP);
  hint.textContent = (state.activeIP && state.activeIP !== SYS)
    ? `发送给：${palName(p)} (${state.activeIP})`
    : '未选择对象：请先在左侧点选好友，或勾选多人群发';
}

async function sendMessage() {
  const input = $('#input');
  const text = input.value.trim();
  if (!text) return;
  try {
    if (state.broadcastMode) {
      await rpc('broadcast', { text });
      appendLine(SYS, { cls: '', who: '我(广播)', text, mine: true });
    } else if (state.selected.size > 0) {
      await rpc('send_msg', { ips: [...state.selected], text });
      appendLine(SYS, { cls: '', who: `我(群发${state.selected.size}人)`, text, mine: true });
    } else if (state.activeIP && state.activeIP !== SYS) {
      await rpc('send_msg', { ips: [state.activeIP], text });
      appendLine(state.activeIP, { cls: '', who: '我', text, mine: true });
    } else {
      appendLine(SYS, { cls: 'sys', text: '请先选择好友（左侧点选或勾选）' });
      return;
    }
    input.value = '';
  } catch (e) {
    appendLine(SYS, { cls: 'sys', text: '发送失败（输入未清空）: ' + e.message });
  }
}

$('#btn-send').addEventListener('click', sendMessage);
$('#input').addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) {
    ev.preventDefault();
    sendMessage();
  }
});
$('#btn-broadcast').addEventListener('click', () => {
  state.broadcastMode = !state.broadcastMode;
  $('#btn-broadcast').classList.toggle('active', state.broadcastMode);
  renderTargetHint();
});

// ---- 工具栏 ----
$('#btn-refresh').addEventListener('click', async () => {
  try { await rpc('refresh'); } catch (e) { alert('刷新失败: ' + e.message); }
});
$('#btn-away').addEventListener('click', async () => {
  state.away = !state.away;
  $('#btn-away').classList.toggle('active', state.away);
  try {
    await rpc('set_absence', { on: state.away });
  } catch (e) {
    state.away = !state.away;
    $('#btn-away').classList.toggle('active', state.away);
    alert(e.message);
  }
});
$('#btn-trans').addEventListener('click', () => $('#trans-panel').classList.toggle('hidden'));
$('#btn-trans-close').addEventListener('click', () => $('#trans-panel').classList.add('hidden'));

$('#btn-file').addEventListener('click', async () => {
  const ips = state.selected.size > 0 ? [...state.selected]
    : (state.activeIP && state.activeIP !== SYS ? [state.activeIP] : []);
  if (!ips.length) { alert('请先选择要发送给谁（左侧点选或勾选）'); return; }
  const paths = await window.feiq.pickFiles();
  if (!paths.length) return;
  try {
    await rpc('send_files', { ips, paths });
    const names = paths.map((p) => p.split('/').pop()).join('、');
    const targets = ips.map((ip) => palName(state.pals.get(ip))).join('、');
    appendLine(SYS, { cls: 'sys', text: `已向 ${targets} 通告文件：${names}，等待对方接收` });
  } catch (e) {
    alert('发送失败: ' + e.message);
  }
});

// ---- 传输面板 ----
let transSeq = 0;
const transKeyOf = (() => {
  const map = new Map(); // Task|Label -> 内部 id
  return (d) => {
    const k = d.Task + '\u0001' + d.Label;
    if (!map.has(k)) map.set(k, ++transSeq);
    return map.get(k);
  };
})();

function renderTrans() {
  const list = $('#trans-list');
  const items = [...state.trans.values()].slice(-30);
  list.innerHTML = items.map((t, i) => {
    const pct = t.total > 0 ? Math.min(100, Math.round(t.done * 100 / t.total)) : 0;
    const stat = t.state === 'done' ? '完成'
      : t.state === 'error' ? '失败: ' + t.err
      : (t.total > 0 ? `${humanSize(t.done)} / ${humanSize(t.total)}` : humanSize(t.done));
    return `<div class="trans-item ${t.state}">
      <div class="row1"><span class="label">${esc(t.label)}</span><span class="state">${esc(stat)}</span></div>
      ${t.state === 'progress' && t.total > 0 ? `<div class="progress"><div data-i="${i}"></div></div>` : ''}
    </div>`;
  }).join('');
  // 宽度走 CSSOM：CSP 不放行 innerHTML 里的 style 属性
  items.forEach((item, i) => {
    if (item.state === 'progress' && item.total > 0) {
      const bar = list.querySelector(`.progress > div[data-i="${i}"]`);
      if (bar) bar.style.width = Math.min(100, item.done * 100 / item.total) + '%';
    }
  });
  // 状态栏摘要
  const doing = items.filter((t) => t.state === 'progress');
  $('#status-transfer').textContent = doing.length
    ? `传输中 ${doing.length} 项`
    : (items.length && items[items.length - 1].state === 'done' ? '最近传输已完成' : '无传输任务');
}

// ---- 事件处理 ----
window.feiq.onEvent(async (name, d) => {
  switch (name) {
    case 'pal': {
      if (d.Off) {
        const p = state.pals.get(d.Pal && d.Pal.IP);
        state.pals.delete(d.Pal.IP);
        appendLine(d.Pal.IP, { cls: 'sys', text: `${(p && p.Name) || d.Pal.Name || d.Pal.IP} 已下线` });
      } else {
        const old = state.pals.get(d.Pal.IP);
        state.pals.set(d.Pal.IP, { ...d.Pal, Unread: old ? old.Unread : 0 });
        if (!old) appendLine(d.Pal.IP, { cls: 'sys', text: `${palName(d.Pal)} 上线` });
      }
      renderRoster();
      renderTabs();
      renderStatus();
      break;
    }
    case 'msg': {
      const pal = d.Pal || {};
      // 事件里的 Unread 恒为 0（后端不维护），保留本地累计值
      state.pals.set(pal.IP, { ...pal, Unread: (state.pals.get(pal.IP) || {}).Unread || 0 });
      const tag = d.Broadcast ? '[广播] ' : '';
      appendLine(pal.IP, { cls: '', who: palName(pal), text: tag + d.Text });
      if (state.activeIP !== pal.IP) {
        const p = state.pals.get(pal.IP);
        if (p) p.Unread = (p.Unread || 0) + 1;
      }
      renderRoster();
      renderTabs();
      break;
    }
    case 'ack': {
      appendLine(d.IP, { cls: 'sys', text: '对方已收到' });
      break;
    }
    case 'offer': {
      offerQueue.push(d);
      drainOffers();
      break;
    }
    case 'trans': {
      const key = transKeyOf(d);
      const cur = state.trans.get(key);
      if (d.State === 'done' && d.Total < 0 && cur) {
        state.trans.set(key, { ...cur, state: 'done' });
      } else {
        state.trans.set(key, { key, label: d.Label, task: d.Task, done: d.Done, total: d.Total, state: d.State, err: d.Err });
      }
      if (state.trans.size > 200) { // 只留最近 200 条
        const drop = [...state.trans.keys()].slice(0, state.trans.size - 200);
        drop.forEach((k) => state.trans.delete(k));
      }
      renderTrans();
      break;
    }
    case 'notice': {
      appendLine(SYS, { cls: 'sys', text: d.Text });
      break;
    }
    case 'backend-dead': {
      backendDead = true;
      appendLine(SYS, { cls: 'sys', text: '后端服务已退出，请重启应用' });
      $('#status-transfer').textContent = '后端已退出';
      break;
    }
  }
});

const offerQueue = [];
let offerDraining = false;
async function drainOffers() {
  if (offerDraining) return;
  offerDraining = true;
  try {
    while (offerQueue.length) await handleOffer(offerQueue.shift());
  } finally {
    offerDraining = false;
  }
}

async function handleOffer(d) {
  const pal = d.Pal || {};
  const files = d.Files || [];
  const total = files.reduce((s, f) => s + (f.Attr === 2 ? 0 : (f.Size || 0)), 0);
  const dirCount = files.filter((f) => f.Attr === 2).length;
  const names = files.map((f) => f.Name + (f.Attr === 2 ? '/' : '')).join('、');
  const sizeText = humanSize(total) + (dirCount ? ` + ${dirCount} 个目录（大小接收时统计）` : '');
  const summary = `来自：${palName(pal)} (${pal.IP})\n文件：${names}\n大小：${sizeText}\n保存到：${state.self ? state.self.download_dir : ''}`;
  appendLine(pal.IP, { cls: 'sys', text: `对方发送文件：${names}（${sizeText}）` });
  const accept = await window.feiq.confirmOffer(summary);
  if (accept) {
    try {
      await rpc('accept_files', { packet_no: d.PacketNo });
      appendLine(pal.IP, { cls: 'sys', text: '开始接收…' });
    } catch (e) {
      appendLine(pal.IP, { cls: 'sys', text: '接收失败: ' + e.message });
    }
  } else {
    try {
      await rpc('reject_files', { packet_no: d.PacketNo, ip: pal.IP });
      appendLine(pal.IP, { cls: 'sys', text: '已拒绝接收' });
    } catch (e) { /* 忽略 */ }
  }
}

// ---- 状态同步 ----
function renderStatus() {
  const n = [...state.pals.values()].filter((p) => p.Online).length;
  $('#status-online').textContent = `在线好友 ${n}`;
}

function syncState(r) {
  if (!r) return;
  if (r.self) {
    state.self = r.self;
    $('#self-name').textContent = r.self.nickname + (r.self.group ? ' · ' + r.self.group : '');
  }
  if (Array.isArray(r.pals)) {
    // 以服务端为准全量重建，仅保留本地未读计数（顺带清掉幽灵好友）
    const next = new Map();
    for (const p of r.pals) {
      next.set(p.IP, { ...p, Unread: (state.pals.get(p.IP) || {}).Unread || 0 });
    }
    state.pals = next;
    renderRoster();
    renderTabs();
    renderStatus();
  }
}

function renderAll() {
  renderRoster();
  renderTabs();
  renderChat();
  renderTargetHint();
}

let backendDead = false;

// 预览模式 mock 实现（由文件开头的守卫安装）
function installPreviewMock() {
  window.feiq = {
    __preview: true,
    rpc: async (method) => {
      if (method === 'get_state') return { result: {
        self: { nickname: 'LUBENTAO', group: '我的好友', download_dir: '~/Downloads' },
        pals: [
          { IP: '192.168.1.20', Name: '张三', Host: 'PC-ZHANG', Group: '我的好友', Online: true },
          { IP: '192.168.1.21', Name: '李四', Host: 'MAC-LI', Group: '我的好友', Online: true, Unread: 2 },
          { IP: '100.106.3.7', Name: '王五', Host: 'TS-WANG', Group: 'Tailscale', Online: true, Absence: true },
          { IP: '192.168.1.30', Name: '赵六', Host: 'PC-ZHAO', Group: '同事', Online: false },
        ],
      } };
      if (method === 'load_history') return { result: { lines: ['—— 以上为历史记录 ——'] } };
      return { result: {} };
    },
    onEvent: () => {},
    pickFiles: async () => [],
    confirmOffer: async () => false,
  };
}

async function init() {
  const preview = !!window.feiq.__preview;
  // 系统会话常驻
  state.tabs = [SYS];
  state.activeIP = SYS;
  try {
    const r = await rpc('get_state');
    syncState(r);
  } catch (e) {
    appendLine(SYS, { cls: 'sys', text: '连接后端失败: ' + e.message });
  }
  if (preview) {
    appendLine(SYS, { cls: 'sys', text: '预览模式：以下为示例数据' });
    openSession('192.168.1.20');
    appendLine('192.168.1.20', { cls: '', who: '张三', text: '在吗？帮忙发个文件' });
    appendLine('192.168.1.20', { cls: '', who: '我', text: '在的，马上传给你', mine: true });
    appendLine('192.168.1.20', { cls: 'sys', text: '开始接收 测试文档.zip（1.2MB）' });
    state.trans.set(1, { key: 1, label: '测试文档.zip', task: 'recv-1', done: 716800, total: 1258291, state: 'progress' });
    state.trans.set(2, { key: 2, label: '设计稿.png', task: 'send-1', done: 512000, total: 512000, state: 'done' });
    renderTrans();
  }
  renderAll();
  // 轮询兜底：事件可能丢（通道满），每 8 秒全量同步一次；后端死后停止
  setInterval(async () => {
    if (backendDead) return;
    try { syncState(await rpc('get_state')); } catch (e) { /* 后端可能已退出 */ }
  }, 8000);
}

init();
