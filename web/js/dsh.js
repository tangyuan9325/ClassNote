// DSH 运行时面板：装、启停、嵌入。
//
// 为什么要有这一层：DSH 渠道的插件是 npm 包，只有 DSH 自己能加载。
// ClassNote 想「真装、真跑」，就得在本机带一个 DSH 运行时。
import { api } from './api.js';
import { $, el, toast } from './util.js';

let S = null;
let timer = null;
let lastPhase = '';
let frameOpen = false;

export function initDsh(state) {
  S = state;
  const box = $('#dsh-panel');
  if (!box) return;
  bindOnce();
  loadDshStatus();
  // 轮询而不是全靠 SSE：安装过程里 SSE 可能断，状态也得对得上。
  timer = setInterval(() => {
    if (document.hidden) return;
    loadDshStatus();
  }, 3000);
}

function bindOnce() {
  if (bindOnce.done) return;
  bindOnce.done = true;
  $('#dsh-actions')?.addEventListener('click', async (e) => {
    const b = e.target.closest('button[data-dsh]');
    if (!b) return;
    const act = b.dataset.dsh;
    if (act === 'install') return doInstall(b);
    if (act === 'start') return doSimple('/api/dsh/start', 'DSH 已启动', b);
    if (act === 'stop') return doSimple('/api/dsh/stop', 'DSH 已停止', b);
    if (act === 'frame') return toggleFrame();
    if (act === 'log') return toggleLog();
    if (act === 'external') {
      const st = await api.get('/api/dsh/status');
      if (st.url) window.open(st.url, '_blank', 'noopener');
      else toast('DSH 还没启动', 'err');
    }
  });
}

async function loadDshStatus() {
  let st;
  try {
    st = await api.get('/api/dsh/status');
  } catch (e) {
    renderError(e.message);
    return;
  }
  render(st);
}

function renderError(msg) {
  const ph = $('#dsh-phase');
  if (ph) { ph.textContent = '无法连接'; ph.className = 'tag'; }
  const meta = $('#dsh-meta');
  if (meta) meta.textContent = msg;
}

const PHASE_LABEL = {
  idle: '未安装',
  download: '下载 Node',
  installing: '安装运行时',
  starting: '启动中',
  running: '运行中',
  stopped: '已就绪',
  error: '出错',
};

function render(st) {
  const ph = $('#dsh-phase');
  const meta = $('#dsh-meta');
  const acts = $('#dsh-actions');
  if (!ph || !meta || !acts) return;

  const label = PHASE_LABEL[st.phase] || st.phase || '未知';
  ph.textContent = label;
  ph.className = 'tag' + (st.phase === 'running' ? ' ok' : st.phase === 'error' ? ' bad' : '');

  const bits = [];
  if (st.nodeOk) bits.push('Node ' + st.nodeVersion);
  else if (st.minNode) bits.push('需要 Node ≥ ' + st.minNode);
  if (st.dshVersion) bits.push('DSH ' + st.dshVersion);
  if (st.running && st.port) bits.push('127.0.0.1:' + st.port);
  if (st.plugins && st.plugins.length) bits.push(st.plugins.length + ' 个插件');
  meta.textContent = bits.join(' · ');

  // 阶段没变就不重建按钮，避免正在点的按钮被换掉。
  if (lastPhase === st.phase && acts.dataset.running === String(st.running)) return;
  lastPhase = st.phase;
  acts.dataset.running = String(st.running);
  acts.innerHTML = '';

  if (st.busy || st.phase === 'installing' || st.phase === 'download' || st.phase === 'starting') {
    acts.append(el('span', { class: 'hint', text: '处理中…' }));
    showLog(true);
    return;
  }
  if (!st.installed) {
    const b = el('button', { class: 'btn small primary', text: '安装 DSH 运行时' });
    b.dataset.dsh = 'install';
    acts.append(b);
    acts.append(el('span', { class: 'hint', text: '约 600 MB，只需装一次' }));
    return;
  }
  if (st.running) {
    const open = el('button', { class: 'btn small primary', text: frameOpen ? '收起界面' : '在页面内打开' });
    open.dataset.dsh = 'frame';
    const win = el('button', { class: 'btn small ghost', text: '新窗口打开' });
    win.dataset.dsh = 'external';
    const stop = el('button', { class: 'btn small ghost', text: '停止' });
    stop.dataset.dsh = 'stop';
    acts.append(open, win, stop);
  } else {
    const start = el('button', { class: 'btn small primary', text: '启动 DSH' });
    start.dataset.dsh = 'start';
    acts.append(start);
  }
  const logBtn = el('button', { class: 'btn small ghost', text: '日志' });
  logBtn.dataset.dsh = 'log';
  acts.append(logBtn);

  if (st.error && st.phase === 'error') {
    meta.textContent = st.error;
    meta.className = 'hint err';
  } else {
    meta.className = 'hint';
  }
}

async function doInstall(btn) {
  btn.disabled = true;
  btn.textContent = '准备中…';
  try {
    await api.post('/api/dsh/install', {});
    toast('开始安装，进度在下方日志里', 'ok');
    showLog(true);
    loadDshStatus();
  } catch (e) {
    toast(e.message, 'err');
    btn.disabled = false;
    btn.textContent = '安装 DSH 运行时';
  }
}

async function doSimple(path, okMsg, btn) {
  btn.disabled = true;
  try {
    const d = await api.post(path, {});
    toast(okMsg, 'ok');
    if (path.endsWith('/stop')) {
      frameOpen = false;
      const wrap = $('#dsh-frame-wrap');
      if (wrap) { wrap.hidden = true; $('#dsh-frame').src = 'about:blank'; }
    } else if (d.url && !frameOpen) {
      // 启动后直接展开，省一次点击。
      toggleFrame(true);
    }
    loadDshStatus();
  } catch (e) {
    toast(e.message, 'err');
    btn.disabled = false;
  }
}

function toggleFrame(force) {
  const wrap = $('#dsh-frame-wrap');
  const frame = $('#dsh-frame');
  if (!wrap || !frame) return;
  const next = force === undefined ? !frameOpen : force;
  if (next && !frame.src.startsWith('http')) {
    api.get('/api/dsh/status').then((st) => {
      if (!st.url) {
        toast('DSH 还没启动', 'err');
        return;
      }
      frame.src = st.url;
      wrap.hidden = false;
      frameOpen = true;
      loadDshStatus();
    }).catch((e) => toast(e.message, 'err'));
    return;
  }
  wrap.hidden = !next;
  frameOpen = next;
  if (!next) frame.src = 'about:blank';
  loadDshStatus();
}

function showLog(on) {
  const box = $('#dsh-log');
  if (box) box.hidden = !on;
}

function toggleLog() {
  const box = $('#dsh-log');
  if (box) box.hidden = !box.hidden;
}

// appendDshLog 由 SSE 的 dsh_log 事件调用。
export function appendDshLog(d) {
  const box = $('#dsh-log');
  if (!box) return;
  const line = d?.line || '';
  if (!line) return;
  const row = el('div', { class: 'dsh-log-line' + (d.level === 'err' ? ' err' : '') , text: line });
  box.append(row);
  while (box.childElementCount > 300) box.removeChild(box.firstChild);
  box.scrollTop = box.scrollHeight;
}

// installDshPlugin 供「发现」页的按钮调用。
//
// 遇到版本不兼容时，DSH 会拒绝安装。这里问一句再重试，
// 而不是直接抛错——老师有知情权，也有自担风险的选择权。
export async function installDshPlugin(pkg) {
  const st = await api.get('/api/dsh/status');
  if (!st.installed) {
    throw new Error('先装 DSH 运行时（插件页顶部），再装插件');
  }
  try {
    return await api.post('/api/dsh/plugin', { package: pkg, action: 'add' });
  } catch (e) {
    if (!e.incompatible) throw e;
    const ok = confirm(
      e.message + '\n\n' +
      '仍然安装可能让 DSH 崩溃或丢数据，只在你确定要试的时候继续。\n\n要强行安装吗？'
    );
    if (!ok) throw new Error('已取消安装');
    return await api.post('/api/dsh/plugin', { package: pkg, action: 'add', force: true });
  }
}

export function dshReady() {
  return lastPhase === 'running' || lastPhase === 'stopped';
}
