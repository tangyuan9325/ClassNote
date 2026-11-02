import { api } from './api.js';
import { $, el, esc, md } from './util.js';

/**
 * Agent 页：模型选择 + 步骤轨迹 + 人工审批 + 最终回答。
 *
 * 交互参照 DeepSeek Harness：一次提问是一个 turn，turn 里有多步，
 * 每步显示「思考 / 工具 / 结果」，写操作在轨迹里就地弹出确认。
 * 后端见 internal/agent：模型只输出 JSON 动作，宿主负责鉴权与执行。
 */

let S = null;
let hooks = {};
let info = null;
let running = false;
let run = null; // 当前运行卡片

export function initAgent(state, h = {}) {
  S = state;
  hooks = h;
  bind();
}

function bind() {
  if (bind.done) return;
  bind.done = true;
  $('#btn-agent-send')?.addEventListener('click', askAgent);
  $('#btn-agent-clear')?.addEventListener('click', () => {
    if (S.session) {
      S.agentMsgs = [];
      renderAgentPage();
      return;
    }
    S.agentMsgs = [];
    renderAgentPage();
  });
  const input = $('#agent-input');
  input?.addEventListener('input', () => {
    input.style.height = 'auto';
    input.style.height = Math.min(120, input.scrollHeight) + 'px';
  });
  input?.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); askAgent(); }
  });
  document.querySelectorAll('#agent-suggest button').forEach((b) => {
    b.addEventListener('click', () => {
      const i = $('#agent-input');
      if (!i) return;
      i.value = b.dataset.q || b.textContent;
      askAgent();
    });
  });
  const sel = $('#agent-model');
  sel?.addEventListener('change', async () => {
    try {
      await api.post('/api/agent/permission', { model: sel.value });
      if (S.cfg?.agent) S.cfg.agent.model = sel.value;
      toastModel(sel.value);
    } catch (e) { /* 选择失败就留在原值，不打断上课 */ }
  });
}

function toastModel(id) {
  const opt = (info?.models || []).find((m) => m.id === id);
  const hint = $('#agent-hint');
  if (hint) hint.textContent = '当前模型：' + (opt?.name || id || '跟随全局设置');
}

export function refreshAgentContext() {
  if (!info) loadAgentInfo();
  renderAgentContext();
}

// loadAgentInfo 拉一次 Agent 元信息（模型目录、工具、权限）。
export async function loadAgentInfo(force = false) {
  if (info && !force) return info;
  try {
    info = await api.get('/api/agent/info');
  } catch (e) {
    info = null;
  }
  renderModelSelect();
  renderAgentContext();
  return info;
}

function renderModelSelect() {
  const sel = $('#agent-model');
  if (!sel) return;
  const models = info?.models || [];
  if (!models.length) {
    sel.innerHTML = '<option value="">跟随全局设置</option>';
    return;
  }
  const cur = S?.cfg?.agent?.model || '';
  sel.innerHTML = '';
  for (const m of models) {
    const o = el('option', { value: m.id, text: m.name + (m.ready ? '' : '（未配置）') });
    if (m.id === cur) o.selected = true;
    sel.append(o);
  }
}

function renderAgentContext() {
  const box = $('#agent-context');
  if (!box) return;
  const bits = [];
  if (S?.session) {
    bits.push(`<span>本课 <b>${esc(S.session.subject || '')} · ${esc(S.session.title || '')}</b></span>`);
    bits.push(`<span>${(S.session.segments || []).length} 页</span>`);
  } else {
    bits.push('<span>未选中课堂 · 通用助手模式</span>');
  }
  const perm = { all: '全部访问', remind: '写操作需确认', review: '每步确认' }[S?.cfg?.agent?.permission || 'remind'];
  bits.push(`<span>权限 <b>${perm}</b></span>`);
  bits.push(`<span>工具 <b>${(info?.toolNames || []).length}</b></span>`);
  const plugs = info?.grants || [];
  if (plugs.length) bits.push(`<span>插件 <b>${plugs.length}</b></span>`);
  box.innerHTML = bits.join('<span style="opacity:.35">·</span>');
}

// renderAgentPage 重建对话（不含进行中的运行卡片）。
export function renderAgentPage() {
  loadAgentInfo();
  renderAgentContext();
  const thread = $('#agent-thread');
  if (!thread) return;
  thread.innerHTML = '';
  run = null;
  const msgs = messages();
  if (!msgs.length) {
    thread.innerHTML = '<div class="side-empty">问点什么吧。比如「这节课的重点是什么」，Agent 会先读记录再回答。</div>';
    return;
  }
  for (const m of msgs) appendMsg(thread, m.role, m.content);
  thread.scrollTop = thread.scrollHeight;
}

function messages() {
  if (S?.session?.messages?.length) {
    return S.session.messages.map((m) => ({ role: m.role, content: m.content }));
  }
  return S?.agentMsgs || [];
}

function appendMsg(thread, role, text) {
  const b = el('div', { class: 'agent-msg ' + (role === 'user' ? 'user' : 'assistant') });
  b.innerHTML = `<span class="who">${role === 'user' ? '你' : 'Agent'}</span>` + (role === 'user' ? esc(text) : md(text));
  thread.append(b);
  thread.scrollTop = thread.scrollHeight;
  return b;
}

// ensureRun 建一张运行卡片，用于承载步骤轨迹。
function ensureRun() {
  const thread = $('#agent-thread');
  if (!thread) return null;
  if (run && run.isConnected) return run;
  run = el('div', { class: 'agent-run' });
  run.innerHTML = '<div class="agent-steps"></div>';
  thread.append(run);
  thread.scrollTop = thread.scrollHeight;
  return run;
}

function stepCard(step) {
  const card = el('div', { class: 'agent-step' });
  card.dataset.step = String(step.index);
  card.innerHTML = `
    <div class="agent-step-head"><b>第 ${step.index} 步</b><span>${esc(step.tool || '思考')}</span><span style="margin-left:auto" data-role="state">进行中…</span></div>
    ${step.thought ? `<div class="agent-step-body" data-role="thought">${esc(step.thought)}</div>` : ''}`;
  return card;
}

function findStep(index) {
  const box = ensureRun()?.querySelector('.agent-steps');
  if (!box) return null;
  let card = box.querySelector(`.agent-step[data-step="${index}"]`);
  if (!card) {
    card = stepCard({ index, tool: '' });
    box.append(card);
  }
  return card;
}

export function agentEvent(type, data) {
  if (!running) return;
  if (type === 'agent_step') {
    findStep(data.index);
    return;
  }
  if (type === 'agent_tool') {
    const card = findStep(data.index);
    if (!card) return;
    card.classList.add('ok');
    card.querySelector('.agent-step-head b').textContent = `第 ${data.index} 步`;
    card.querySelector('.agent-step-head span:nth-child(2)').textContent = data.tool;
    card.querySelector('[data-role="state"]').textContent = '调用中…';
    if (data.thought) {
      let t = card.querySelector('[data-role="thought"]');
      if (!t) {
        t = el('div', { class: 'agent-step-body', dataset: { role: 'thought' } });
        card.append(t);
      }
      t.textContent = data.thought;
    }
    return;
  }
  if (type === 'agent_tool_result') {
    const card = findStep(data.index);
    if (!card) return;
    const isErr = !!data.error;
    card.classList.toggle('err', isErr);
    card.querySelector('[data-role="state"]').textContent = isErr ? '失败' : '完成';
    let body = card.querySelector('[data-role="result"]');
    if (!body) {
      body = el('div', { class: 'agent-step-body', dataset: { role: 'result' } });
      card.append(body);
    }
    body.textContent = data.error || data.result || '';
    return;
  }
  if (type === 'agent_approval') {
    renderApproval(data);
    return;
  }
  if (type === 'agent_final') {
    const thread = $('#agent-thread');
    if (thread && data.answer) {
      const b = appendMsg(thread, 'assistant', data.answer);
      b.dataset.final = '1';
    }
    return;
  }
  if (type === 'agent_error') {
    const card = findStep(data.step || 1);
    if (card) {
      card.classList.add('err');
      card.querySelector('[data-role="state"]').textContent = '出错';
      card.append(el('div', { class: 'agent-step-body', text: data.message || '未知错误' }));
    }
  }
}

function renderApproval(req) {
  const card = ensureRun();
  if (!card) return;
  const box = el('div', { class: 'agent-approval' });
  box.innerHTML = `
    <b>要执行写操作：${esc(req.title || req.tool)}</b>
    <pre>${esc(req.preview || req.args || '')}</pre>
    <div class="row">
      <button class="btn small primary" data-act="allow">允许这一次</button>
      <button class="btn small ghost" data-act="deny">拒绝</button>
    </div>`;
  const decide = async (allow) => {
    box.querySelectorAll('button').forEach((b) => { b.disabled = true; });
    try {
      await api.post('/api/agent/approve', { id: req.id, allow });
    } catch (e) { /* 超时后服务端会自己拒掉 */ }
    if (allow) {
      box.querySelector('[data-act="allow"]').textContent = '已允许';
    } else {
      box.querySelector('[data-act="deny"]').textContent = '已拒绝';
    }
  };
  box.querySelector('[data-act="allow"]').addEventListener('click', () => decide(true));
  box.querySelector('[data-act="deny"]').addEventListener('click', () => decide(false));
  card.append(box);
  card.scrollIntoView({ block: 'nearest' });
}

export async function askAgent() {
  const input = $('#agent-input');
  const q = (input?.value || '').trim();
  if (!q || running) return;
  running = true;
  const btn = $('#btn-agent-send');
  if (btn) btn.disabled = true;
  input.value = '';
  input.style.height = 'auto';

  const thread = $('#agent-thread');
  if (thread?.querySelector('.side-empty')) thread.innerHTML = '';
  appendMsg(thread, 'user', q);
  ensureRun();
  const head = el('div', { class: 'agent-step' });
  head.innerHTML = '<div class="agent-step-head"><b>开始</b><span>正在读课堂材料…</span></div>';
  ensureRun().querySelector('.agent-steps').append(head);

  const history = messages().slice(-8).map((m) => ({ role: m.role, content: m.content }));
  const model = $('#agent-model')?.value || '';

  try {
    const d = await api.post('/api/agent/run', {
      question: q, sessionId: S?.session?.id || '', model, history,
    });
    const answer = d.answer || '';
    // 流式阶段已经把最终回答插进 thread 了；没有的话补一条。
    const hasFinal = thread.querySelector('[data-final="1"]');
    if (!hasFinal && answer) appendMsg(thread, 'assistant', answer);
    if (answer) {
      if (S?.session) {
        S.session.messages = S.session.messages || [];
        S.session.messages.push({ role: 'user', content: q, createdAt: new Date().toISOString() });
        S.session.messages.push({ role: 'assistant', content: answer, createdAt: new Date().toISOString() });
      } else {
        S.agentMsgs = S.agentMsgs || [];
        S.agentMsgs.push({ role: 'user', content: q });
        S.agentMsgs.push({ role: 'assistant', content: answer });
      }
    }
    if (d.stop && d.stop !== 'final') {
      const note = el('div', { class: 'agent-step' });
      note.innerHTML = `<div class="agent-step-head"><b>结束</b><span>${esc(stopText(d.stop))}</span></div>`;
      ensureRun()?.append(note);
    }
    if (typeof hooks.refreshSession === 'function') hooks.refreshSession();
  } catch (e) {
    const card = ensureRun();
    if (card) {
      card.append(el('div', { class: 'agent-approval', text: '运行失败：' + e.message }));
    }
    S.agentMsgs = S.agentMsgs || [];
    S.agentMsgs.push({ role: 'user', content: q });
  } finally {
    running = false;
    run = null;
    if (btn) btn.disabled = false;
    input?.focus();
  }
}

function stopText(stop) {
  switch (stop) {
    case 'max-steps': return '达到步数上限，已给出阶段性结论';
    case 'cancelled': return '已取消';
    case 'llm-error': return '模型调用出错';
    default: return stop;
  }
}

export function isAgentRunning() { return running; }
