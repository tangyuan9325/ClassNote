// 更新面板：检查版本、加速节点测速、下载进度、安装重启。
//
// 单独成模块的理由和 asrmodels 一样：设置页切标签会整块重绘，
// 而下载可能跑好几分钟、测速也要十秒，进度不能因为重绘就丢掉。
import { api } from './api.js';
import { $, toast, esc } from './util.js';

let cfgRef = null;
let timer = 0;

const state = {
  info: null, // Check 的结果
  nodes: [],
  cached: false,
  task: { state: 'idle' },
  last: null,
  hasLast: false,
  running: false,
  mirror: 'auto',
  speedTestOff: false,
  lastNode: '',
  downloadedPath: '',
  testing: false,
};

const mb = (n) => (n >= 1 << 30 ? (n / (1 << 30)).toFixed(1) + ' GB' : Math.max(1, Math.round(n / (1 << 20))) + ' MB');
const kb = (n) => (n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MB/s' : Math.round(n / 1024) + ' KB/s');

function speedText(bps) {
  if (!bps) return '—';
  return bps >= 1 << 20 ? (bps / (1 << 20)).toFixed(1) + ' MB/s' : Math.round(bps / 1024) + ' KB/s';
}

// updatePanelHtml 渲染整块骨架（状态由 render 填写）。
export function updatePanelHtml(cfg) {
  const g = cfg.github || {};
  return `
    <h3>更新</h3>
    <div class="field-row two">
      <label class="field"><span>GitHub 仓库</span>
        <input id="f-repo" value="${esc(g.repo || '')}" placeholder="owner/name">
      </label>
      <label class="field"><span>Token（可选，私有仓库需要）</span>
        <input id="f-ghtoken" type="password" value="${esc(g.token || '')}" placeholder="ghp_...">
      </label>
    </div>
    <div class="desc">从 GitHub Releases 检查新版本。国内直连 GitHub 下载常常只有几十 KB/s，下载时会自动挑选最快的加速节点。</div>

    <div class="up-toolbar">
      <button class="btn small primary" id="btn-update-check">检查更新</button>
      <button class="btn small ghost" id="btn-update-speedtest">节点测速</button>
      <label class="up-inline"><span>节点策略</span>
        <select id="f-upmirror">
          <option value="auto">自动测速（推荐）</option>
          <option value="off">只连 GitHub</option>
        </select>
      </label>
      <label class="up-inline up-auto">
        <span>启动时自动检查</span>
        <span class="switch"><input type="checkbox" id="f-checkupdates" ${g.checkUpdates ? 'checked' : ''}><i></i></span>
      </label>
    </div>

    <div class="up-nodes" id="up-nodes">
      <div class="up-node-head"><span>加速节点</span></div>
      <div class="up-empty">还没有节点信息。点「检查更新」或「节点测速」获取。</div>
    </div>

    <div class="up-result" id="up-result" hidden></div>

    <div class="asr-prog up-prog" id="up-prog" hidden>
      <div class="asr-bar"><i id="up-bar-fill" style="width:0%"></i></div>
      <div class="asr-prog-meta">
        <span id="up-prog-text">0%</span>
        <button class="btn small ghost" id="up-cancel">取消</button>
      </div>
    </div>

    <div class="row up-actions" id="up-actions" hidden>
      <button class="btn small primary" id="btn-update-download">下载更新包</button>
      <button class="btn small primary" id="btn-update-apply" hidden>安装并重启</button>
      <button class="btn small ghost" id="btn-update-page">打开发布页</button>
    </div>

    <details class="up-adv">
      <summary>高级：自定义加速节点</summary>
      <div class="field">
        <label>每行一个 https 前缀，例如 https://ghfast.top/</label>
        <textarea id="f-upmirrors" rows="3" placeholder="https://your-proxy.example/">${esc((g.mirrors || []).join('\n'))}</textarea>
        <div class="desc">自定义节点同样参与测速。私有仓库带 Token 时只走直连——第三方节点拿不到你的鉴权。</div>
      </div>
      <div class="desc" id="up-last"></div>
    </details>`;
}

// loadUpdateState 拉一次状态并渲染。
export async function loadUpdateState() {
  try {
    const d = await api.get('/api/update/status');
    state.task = d.task || { state: 'idle' };
    state.last = d.last || null;
    state.hasLast = !!d.hasLast;
    state.running = !!d.running;
    state.mirror = d.mirror || 'auto';
    state.speedTestOff = !!d.speedTestOff;
    state.lastNode = d.lastNode || '';
  } catch (e) {
    // 状态拉不到不影响检查更新，静默即可。
  }
  render();
  schedule();
}

// bindUpdate 绑定交互，设置页打开「关于」标签时调用。
export function bindUpdate(cfg) {
  cfgRef = cfg;
  const g = cfg.github || (cfg.github = {});

  $('#f-repo')?.addEventListener('change', (e) => { g.repo = e.target.value.trim(); save(); });
  $('#f-ghtoken')?.addEventListener('change', (e) => { g.token = e.target.value.trim(); save(); });
  $('#f-checkupdates')?.addEventListener('change', (e) => { g.checkUpdates = e.target.checked; save(); });
  $('#f-upmirror')?.addEventListener('change', (e) => {
    g.mirror = e.target.value;
    state.mirror = e.target.value;
    save();
    toast(e.target.value === 'off' ? '只用直连 GitHub' : '自动测速选节点', 'ok');
  });
  $('#f-upmirrors')?.addEventListener('change', (e) => {
    g.mirrors = e.target.value.split('\n').map((s) => s.trim()).filter(Boolean);
    save();
    toast('自定义节点已保存，点「节点测速」验证', 'ok');
  });

  $('#btn-update-check')?.addEventListener('click', checkNow);
  $('#btn-update-speedtest')?.addEventListener('click', () => speedTest(true));
  $('#btn-update-download')?.addEventListener('click', download);
  $('#up-cancel')?.addEventListener('click', async () => {
    await api.post('/api/update/cancel', {});
    toast('已取消下载', '');
    loadUpdateState();
  });
  $('#btn-update-apply')?.addEventListener('click', apply);
  $('#btn-update-page')?.addEventListener('click', () => {
    const url = state.info?.url;
    if (url) window.open(url, '_blank');
  });

  renderMirrorSelect();
  loadUpdateState();
  // 打开面板时顺手把上次检查结果补回来（页面重绘不会丢）。
  if (state.info) render();
}

// save 直接落盘配置：更新面板改动的是 github 段，不影响其它设置。
function save() {
  if (!cfgRef) return;
  api.post('/api/config', cfgRef).catch(() => {});
}

function renderMirrorSelect() {
  const sel = $('#f-upmirror');
  if (!sel) return;
  const cur = (cfgRef?.github?.mirror) || 'auto';
  const nodes = state.nodes.length ? state.nodes : [];
  const opts = ['<option value="auto">自动测速（推荐）</option>', '<option value="off">只连 GitHub</option>'];
  const ids = ['auto', 'off'];
  for (const n of nodes) {
    opts.push(`<option value="${esc(n.id)}">固定：${esc(n.name)}</option>`);
    ids.push(n.id);
  }
  sel.innerHTML = opts.join('');
  sel.value = ids.includes(cur) ? cur : 'auto';
}

// checkNow 检查新版本，并带上节点缓存。
async function checkNow() {
  const box = $('#up-result');
  if (box) {
    box.hidden = false;
    box.className = 'up-result';
    box.textContent = '检查中…';
  }
  try {
    const d = await api.get('/api/update/check');
    state.info = d.info || null;
    state.nodes = d.nodes || [];
    state.cached = !!d.cached;
    state.running = !!d.running;
    render();
    if (d.info?.error) toast(d.info.error, 'warn');
  } catch (e) {
    if (box) {
      box.className = 'up-result err';
      box.textContent = e.message;
    }
  }
}

// speedTest 对所有候选节点跑一次测速。
async function speedTest(force) {
  const url = pickAsset()?.url;
  if (!url) {
    toast('先检查更新，拿到更新包地址后再测速', 'warn');
    return;
  }
  if (state.testing) return;
  state.testing = true;
  const btn = $('#btn-update-speedtest');
  if (btn) { btn.disabled = true; btn.textContent = '测速中…'; }
  try {
    const d = await api.post('/api/update/nodes', { url, force: !!force });
    state.nodes = d.nodes || [];
    state.cached = !!d.cached;
    renderMirrorSelect();
    render();
    const ok = state.nodes.filter((n) => n.ok).length;
    toast(ok ? `${ok} 个节点可用，已按速度排序` : '所有节点都测不通，仍可直连试试', ok ? 'ok' : 'warn');
  } catch (e) {
    toast('测速失败：' + e.message, 'warn');
  } finally {
    state.testing = false;
    if (btn) { btn.disabled = false; btn.textContent = '节点测速'; }
  }
}

function pickAsset() {
  const assets = state.info?.assets || [];
  return assets.find((a) => /windows-amd64\.zip$/i.test(a.name)) || assets.find((a) => /\.zip$/i.test(a.name)) || assets[0];
}

// download 启动后台下载（进度走 SSE）。
async function download() {
  const asset = pickAsset();
  if (!asset) {
    toast('这个 Release 没有可下载的更新包，请到发布页手动下载', 'warn');
    return;
  }
  const sel = $('#f-upmirror');
  try {
    const d = await api.post('/api/update/download', {
      url: asset.url, name: asset.name, size: asset.size || 0,
      node: sel && sel.value !== 'auto' ? sel.value : '',
    });
    toast(d.first ? `开始下载（${d.first}）` : '开始下载', 'ok');
    state.task = d.task || { state: 'probing' };
    render();
    schedule();
  } catch (e) {
    toast('下载失败：' + e.message, 'warn');
  }
}

// apply 安装并重启。
async function apply() {
  const path = state.downloadedPath || state.task?.path;
  if (!path) {
    toast('还没有下载好的更新包', 'warn');
    return;
  }
  if (state.running) {
    toast('正在上课，结束课堂后再安装', 'warn');
    return;
  }
  if (!confirm('将退出程序、替换文件并自动重启。建议先结束当前课堂。继续吗？')) return;
  try {
    await api.post('/api/update/apply', { path, version: state.info?.latest || '' });
    document.body.innerHTML =
      '<div style="display:flex;height:100vh;align-items:center;justify-content:center;flex-direction:column;gap:12px;font-family:system-ui,sans-serif;color:#666">' +
      '<div style="font-size:18px">正在安装更新…</div>' +
      '<div style="font-size:13px">程序已退出，升级助手正在替换文件，完成后会自动重新打开。</div></div>';
  } catch (e) {
    toast('安装失败：' + e.message, 'warn');
  }
}

// onUpdateProgress 处理 SSE 推来的 update_progress。
export function onUpdateProgress(p) {
  if (!p) return;
  const prev = state.task?.state;
  state.task = p;
  if (p.state === 'done') {
    state.downloadedPath = p.path || '';
    toast('更新包已下载完成，可以安装', 'ok');
    render();
    schedule();
    return;
  }
  if (p.state === 'error' && prev !== 'error') {
    toast('下载失败：' + (p.error || '未知错误') + '（可点「节点测速」换一个节点）', 'warn');
  }
  render();
  schedule();
}

// schedule 任务进行中时定时刷新，兜住 SSE 断线的情况。
function schedule() {
  clearTimeout(timer);
  const s = state.task?.state;
  if (s === 'probing' || s === 'downloading' || s === 'verifying') {
    timer = setTimeout(loadUpdateState, 2500);
  }
}

// ---------------- 渲染 ----------------

function render() {
  if (!document.getElementById('up-nodes')) return;
  renderNodes();
  renderResult();
  renderProgress();
  renderLast();
}

function renderNodes() {
  const box = $('#up-nodes');
  if (!box) return;
  const nodes = state.nodes || [];
  const head = `<div class="up-node-head">
      <span>加速节点</span>
      <span class="hint">${state.cached ? '（缓存结果，可重新测速）' : state.speedTestOff ? '（已关闭自动测速）' : ''}</span>
    </div>`;
  if (!nodes.length) {
    box.innerHTML = head + '<div class="up-empty">还没有节点信息。点「检查更新」或「节点测速」获取。</div>';
    return;
  }
  const rows = nodes.map((n, i) => {
    const best = i === 0 && n.ok;
    const tag = !n.tested
      ? '<span class="tag">未测速</span>'
      : n.ok
        ? `<span class="tag ok">${speedText(n.speed)}</span>`
        : `<span class="tag bad">${esc(n.error || '不可用')}</span>`;
    return `<div class="up-node${best ? ' on' : ''}">
        <div class="up-node-name"><b>${esc(n.name)}</b>${n.custom ? '<span class="tag">自定义</span>' : ''}${best ? '<span class="tag ok">最快</span>' : ''}</div>
        <div class="up-node-note">${esc(n.note || '')}</div>
        <div class="up-node-meta">
          <span>${n.tested && n.ok ? `首字 ${n.latency} ms` : '—'}</span>
          <span>${n.size ? mb(n.size) : ''}</span>
          ${tag}
        </div>
      </div>`;
  }).join('');
  box.innerHTML = head + rows;
}

function renderResult() {
  const box = $('#up-result');
  const actions = $('#up-actions');
  if (!box) return;
  const info = state.info;
  if (!info) {
    box.hidden = true;
    if (actions) actions.hidden = true;
    return;
  }
  box.hidden = false;
  if (info.error) {
    box.className = 'up-result err';
    box.textContent = info.error;
    if (actions) actions.hidden = true;
    return;
  }
  const asset = pickAsset();
  const lines = [];
  if (info.hasUpdate) {
    lines.push(`发现新版本 ${info.latest}（当前 ${info.current}）`);
  } else {
    lines.push(`已是最新版本（${info.current}）`);
  }
  if (info.source) lines.push(`检查方式：${info.source}`);
  if (info.publishedAt) lines.push(`发布时间：${info.publishedAt}`);
  if (asset) lines.push(`更新包：${asset.name}（${mb(asset.size || 0)}）`);
  else if (info.hasUpdate) lines.push('这个 Release 里没有找到更新包，请到发布页手动下载。');
  box.className = info.hasUpdate ? 'up-result ok' : 'up-result';
  box.innerHTML =
    `<div class="up-ver">${esc(lines.join(' · '))}</div>` +
    (info.notes ? `<pre class="up-notes">${esc(info.notes.slice(0, 1200))}</pre>` : '');
  if (actions) {
    actions.hidden = !info.hasUpdate;
    const dl = $('#btn-update-download');
    if (dl) dl.disabled = !asset;
    const page = $('#btn-update-page');
    if (page) page.disabled = !info.url;
  }
  renderMirrorSelect();
}

function renderProgress() {
  const box = $('#up-prog');
  if (!box) return;
  const t = state.task || { state: 'idle' };
  const busy = t.state === 'probing' || t.state === 'downloading' || t.state === 'verifying';
  box.hidden = !busy && t.state !== 'done';
  const fill = $('#up-bar-fill');
  const text = $('#up-prog-text');
  const done = t.done || 0;
  const total = t.total || 0;
  const pct = total > 0 ? Math.min(100, (done * 100) / total) : 0;
  if (fill) fill.style.width = pct.toFixed(1) + '%';
  if (text) {
    if (t.state === 'probing') {
      text.textContent = '正在测速选节点…';
    } else if (t.state === 'verifying') {
      text.textContent = '校验更新包…';
    } else if (t.state === 'done') {
      text.textContent = `已下载 ${mb(done)}${t.node ? '（' + t.node + '）' : ''}`;
    } else if (t.state === 'error') {
      text.textContent = '失败：' + (t.error || '未知错误');
    } else if (t.state === 'canceled') {
      text.textContent = '已取消';
    } else {
      const left = t.speed > 0 && total > done ? Math.max(1, Math.round((total - done) / t.speed)) : 0;
      text.textContent = `${mb(done)} / ${mb(total)} · ${pct.toFixed(0)}% · ${speedText(t.speed)}` +
        (t.node ? ` · ${t.node}` : '') + (left ? ` · 剩余 ${left}s` : '');
    }
  }
  const cancel = $('#up-cancel');
  if (cancel) cancel.hidden = !busy;
  const applyBtn = $('#btn-update-apply');
  if (applyBtn) applyBtn.hidden = !(t.state === 'done' && t.path);
}

function renderLast() {
  const box = $('#up-last');
  if (!box) return;
  if (!state.hasLast || !state.last) {
    box.textContent = '';
    return;
  }
  const r = state.last;
  const when = (r.at || '').replace('T', ' ').slice(0, 16);
  box.textContent = r.ok
    ? `上次升级：${when} 从 ${r.from || '?'} 到 ${r.to || '?'} 成功（${r.files || 0} 个文件）。
       日志：${r.log || '—'}`
    : `上次升级：${when} 失败 —— ${r.error || '未知原因'}（已回滚到旧版本）。日志：${r.log || '—'}`;
}
