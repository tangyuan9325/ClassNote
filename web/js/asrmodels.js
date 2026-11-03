// 语音模型管理：列表、下载、进度、切换。
//
// 单独一个模块的理由：设置页每次切换标签都会整块重绘，
// 而下载可能跑几分钟——进度条不能因为重绘就丢。这里自己维护
// 一份最新状态，重绘时按状态复原。
import { api } from './api.js';
import { toast } from './util.js';

let state = { models: [], task: { state: 'idle' }, ready: false, active: '', dir: '' };
let timer = 0;

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const mb = (n) => (n >= 1 << 30 ? (n / (1 << 30)).toFixed(1) + ' GB' : Math.round(n / (1 << 20)) + ' MB');
const pct = (a, b) => (b > 0 ? Math.min(100, (a * 100) / b) : 0);

function stars(n) {
  return '●'.repeat(n) + '○'.repeat(Math.max(0, 5 - n));
}

// asrModelsHtml 渲染整块（骨架 + 当前状态）。
export function asrModelsHtml() {
  return `
    <div class="asr-panel" id="asr-panel">
      <div class="asr-head">
        <div class="asr-head-title">
          <b>本地语音</b>
          <span class="tag" id="asr-state">检测中</span>
          <span class="hint" id="asr-meta"></span>
        </div>
        <button class="btn small ghost" id="asr-refresh">刷新</button>
      </div>

      <div class="asr-prog" id="asr-prog" hidden>
        <div class="asr-bar"><i id="asr-bar-fill" style="width:0%"></i></div>
        <div class="asr-prog-meta">
          <span id="asr-prog-text">0%</span>
          <button class="btn small ghost" id="asr-cancel">取消</button>
        </div>
      </div>

      <div class="asr-list" id="asr-list"></div>

      <div class="field-row two">
        <label class="field"><span>下载源</span>
          <select id="f-asrmirror">
            <option value="">自动（国内镜像优先，失败回落官方）</option>
            <option value="https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main">hf-mirror.com（国内）</option>
            <option value="https://huggingface.co/ggerganov/whisper.cpp/resolve/main">huggingface.co（官方）</option>
          </select>
        </label>
        <label class="field"><span>模型目录</span><input id="f-asrdir" readonly></label>
      </div>
      <div class="desc">模型来自 whisper.cpp 官方仓库（MIT）。下载后按 SHA-256 校验，不完整会自动重下。删除模型只影响本地文件，随时可以再下。</div>
    </div>`;
}

// loadASRModels 拉一次状态并渲染。
export async function loadASRModels() {
  const box = document.getElementById('asr-list');
  if (!box) return;
  try {
    const d = await api.get('/api/asr/models');
    state = {
      models: d.models || [],
      task: d.task || { state: 'idle' },
      ready: !!d.ready,
      active: d.active || '',
      dir: d.dir || '',
      port: d.port || 0,
      default: d.default || '',
      mirror: d.mirror || '',
    };
    const sel = document.getElementById('f-asrmirror');
    if (sel) sel.value = d.mirror || '';
    const dirInput = document.getElementById('f-asrdir');
    if (dirInput) dirInput.value = d.dir || '（未找到运行时目录）';
  } catch (e) {
    box.innerHTML = `<div class="asr-empty">读取模型状态失败：${esc(e.message)}</div>`;
    return;
  }
  renderASR();
  scheduleRefresh();
}

// renderASR 按当前 state 重绘。
function renderASR() {
  const st = document.getElementById('asr-state');
  const meta = document.getElementById('asr-meta');
  const list = document.getElementById('asr-list');
  const prog = document.getElementById('asr-prog');
  if (!st || !list) return;

  const t = state.task || { state: 'idle' };
  const running = t.state === 'downloading' || t.state === 'verifying';
  const active = state.models.find((m) => m.active);

  // 顶部状态
  if (running) {
    st.textContent = t.state === 'verifying' ? '校验中' : '下载中';
    st.className = 'tag';
  } else if (state.ready) {
    st.textContent = '运行中';
    st.className = 'tag ok';
  } else if (active) {
    st.textContent = '已就绪';
    st.className = 'tag';
  } else {
    st.textContent = '未就绪';
    st.className = 'tag bad';
  }
  if (meta) {
    const parts = [];
    if (active) parts.push(`当前 ${esc(active.name)}`);
    if (state.ready) parts.push(`端口 ${state.port || '—'}`);
    meta.innerHTML = parts.join(' · ');
  }

  // 进度条
  if (prog) {
    prog.hidden = !running;
    if (running) {
      const p = pct(t.done || 0, t.total || 0);
      const fill = document.getElementById('asr-bar-fill');
      const text = document.getElementById('asr-prog-text');
      if (fill) fill.style.width = p.toFixed(1) + '%';
      if (text) {
        const spd = t.speed > 0 ? `${(t.speed / (1 << 20)).toFixed(1)} MB/s` : '连接中';
        const left = t.speed > 0 ? Math.max(1, Math.round(((t.total - t.done) / t.speed) / 60)) : 0;
        text.textContent = `${mb(t.done || 0)} / ${mb(t.total || 0)} · ${p.toFixed(0)}% · ${spd}` + (left ? ` · 约 ${left} 分钟` : '');
      }
    }
  }

  // 列表
  list.innerHTML = state.models.map((m) => rowHtml(m, running, state.task)).join('');
  bindRows();
}

function rowHtml(m, running, task) {
  const busyThis = running && task && task.id === m.id;
  const busyOther = running && task && task.id !== m.id;
  let badge = '';
  if (m.active) badge = '<span class="tag ok">使用中</span>';
  else if (m.installed) badge = '<span class="tag">已下载</span>';
  else if (m.partial > 0) badge = '<span class="tag">有缓存</span>';
  if (m.id === state.default && !m.installed) badge += '<span class="tag">默认</span>';

  let actions = '';
  if (busyThis) {
    actions = '<button class="btn small ghost" data-act="cancel">取消</button>';
  } else if (m.installed) {
    actions =
      (m.active
        ? '<button class="btn small ghost" disabled>已在用</button>'
        : '<button class="btn small primary" data-act="use">使用</button>') +
      (m.active ? '' : '<button class="btn small ghost" data-act="del">删除</button>');
  } else {
    actions = `<button class="btn small primary" data-act="dl" ${busyOther ? 'disabled' : ''}>${m.partial > 0 ? '继续下载' : '下载'}</button>`;
    if (m.partial > 0) actions += '<button class="btn small ghost" data-act="del">清除</button>';
  }

  return `
    <div class="asr-item${m.active ? ' on' : ''}" data-id="${esc(m.id)}">
      <div class="asr-main">
        <div class="asr-title">
          <b>${esc(m.name)}</b>
          <span class="tag">${esc(m.tag)}</span>
          ${badge}
        </div>
        <div class="asr-spec">
          <span>${mb(m.size)}</span>
          <span title="中文准确率">中文 ${stars(m.quality)}</span>
          <span title="相对速度">速度 ${stars(m.speed)}</span>
        </div>
        <div class="asr-note">${esc(m.note)}</div>
      </div>
      <div class="asr-act">${actions}</div>
    </div>`;
}

function bindRows() {
  const list = document.getElementById('asr-list');
  if (!list || list.dataset.bound === '1') {
    // 每次重绘都会换掉子节点，所以事件挂在容器上（委托）。
    if (list) {
      list.dataset.bound = '1';
      list.addEventListener('click', onRowClick);
    }
  }
  const cancel = document.getElementById('asr-cancel');
  if (cancel && !cancel.dataset.bound) {
    cancel.dataset.bound = '1';
    cancel.addEventListener('click', async () => {
      await api.post('/api/asr/models/cancel', {});
      toast('已取消下载', '');
      setTimeout(loadASRModels, 400);
    });
  }
  const refresh = document.getElementById('asr-refresh');
  if (refresh && !refresh.dataset.bound) {
    refresh.dataset.bound = '1';
    refresh.addEventListener('click', () => loadASRModels());
  }
}

async function onRowClick(e) {
  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  const row = e.target.closest('.asr-item');
  if (!row) return;
  const id = row.dataset.id;
  const act = btn.dataset.act;
  const m = state.models.find((x) => x.id === id);
  btn.disabled = true;
  try {
    if (act === 'dl') {
      const sel = document.getElementById('f-asrmirror');
      await api.post('/api/asr/models/download', { id, mirror: sel ? sel.value : '' });
      toast(`开始下载 ${m ? m.name : id}`, 'ok');
      loadASRModels();
    } else if (act === 'cancel') {
      await api.post('/api/asr/models/cancel', { id });
      toast('已取消', '');
      setTimeout(loadASRModels, 400);
    } else if (act === 'use') {
      await api.post('/api/asr/models/activate', { id });
      toast(`正在切换到 ${m ? m.name : id}…`, 'ok');
      setTimeout(loadASRModels, 2500);
    } else if (act === 'del') {
      if (!confirm(`删除 ${m ? m.name : id} 的本地文件？之后可以重新下载。`)) {
        btn.disabled = false;
        return;
      }
      await api.post('/api/asr/models/delete', { id });
      toast('已删除', '');
      loadASRModels();
    }
  } catch (err) {
    toast(err.message || '操作失败', 'warn');
    btn.disabled = false;
  }
}

// onASRProgress 处理 SSE 推来的 asr_model 事件。
export function onASRProgress(p) {
  if (!p) return;
  state.task = p;
  if (p.state === 'done') {
    // 下载完成后要重新拉清单（installed 变了）。
    setTimeout(loadASRModels, 500);
    const m = state.models.find((x) => x.id === p.id);
    toast(`语音模型${m ? ' ' + m.name : ''}已就绪`, 'ok');
  } else if (p.state === 'error') {
    toast('语音模型下载失败：' + (p.error || '未知错误'), 'warn');
  } else if (p.state === 'canceled') {
    toast('已取消下载', '');
  }
  renderASR();
}

// scheduleRefresh 下载中时定时刷新，避免 SSE 断线后进度停住。
function scheduleRefresh() {
  clearTimeout(timer);
  const t = state.task || {};
  if (t.state === 'downloading' || t.state === 'verifying') {
    timer = setTimeout(loadASRModels, 3000);
  }
}
