import { api } from './api.js';
import { $, el, esc, fmtDate } from './util.js';
import { openSettings } from './settings.js';

/**
 * 界面样式（骨架）切换与 DSH 布局的附加结构。
 *
 * 说明：DSH 样式下的**布局与度量**参考 DeepSeek Harness
 * （https://github.com/deepseek-ai/deepseek-harness，MIT License），
 * 配色仍用 ClassNote 自己的主题变量。切换只改 <html data-ui>，
 * 不重载页面、不丢当前课堂状态。
 */

export const UI_STYLES = [
  { id: 'legacy', name: '经典', note: 'ClassNote 原有布局：左导航 + 实录 + 右侧画面栏' },
  { id: 'dsh', name: 'DSH', note: '借鉴 DeepSeek Harness 的三栏布局与会话式输入面板' },
];

let S = null;
let onChange = null;

// applyUiStyle 立即切换骨架。
export function applyUiStyle(style) {
  const next = style === 'dsh' ? 'dsh' : 'legacy';
  document.documentElement.dataset.ui = next;
  document.querySelectorAll('.style-switch button').forEach((b) => {
    b.classList.toggle('active', b.dataset.style === next);
  });
  return next;
}

// mountShell 在侧栏挂上会话列表、样式切换器与来源标注。
export function mountShell(state, opts = {}) {
  S = state;
  onChange = opts.onStyleChange || null;
  const rail = $('#rail');
  if (!rail || rail.dataset.mounted === '1') {
    applyUiStyle(S?.cfg?.ui?.style);
    return;
  }
  rail.dataset.mounted = '1';

  // 会话列表（DSH 的左栏是工作区 → 会话）
  const sessions = el('div', { class: 'dsh-sessions', id: 'rail-sessions' });
  sessions.innerHTML = '<div class="dsh-sessions-head"><span>最近课堂</span><span id="rail-sessions-count"></span></div><div id="rail-sessions-list"></div>';
  const foot = rail.querySelector('.rail-foot');

  // 样式切换器
  const sw = el('div', { class: 'style-switch', id: 'style-switch', title: '切换界面样式' });
  for (const s of UI_STYLES) {
    const b = el('button', { dataset: { style: s.id }, title: s.note, text: s.name });
    b.addEventListener('click', () => pickStyle(s.id));
    sw.append(b);
  }
  const credit = el('div', { class: 'dsh-credit', id: 'dsh-credit' });
  credit.innerHTML = '布局参考 <a href="https://github.com/deepseek-ai/deepseek-harness" target="_blank" rel="noreferrer">DeepSeek Harness</a>（MIT）';

  if (foot) {
    foot.insertBefore(sw, foot.firstChild);
    foot.append(credit);
    rail.insertBefore(sessions, foot);
  } else {
    rail.append(sessions, sw, credit);
  }

  // 输入区上方的模型 / 权限胶囊（DSH 把模型选择放在输入面板里）
  const wrap = $('#composer-wrap');
  if (wrap && !$('.dsh-composer-bar')) {
    const bar = el('div', { class: 'dsh-composer-bar', id: 'dsh-composer-bar' });
    wrap.insertBefore(bar, wrap.firstChild);
  }
  renderComposerBar();
  // 标注已经插进 DOM，这里再应用开关状态。
  applyStyleNotice();

  applyUiStyle(S?.cfg?.ui?.style);
  loadRailSessions();
}

// applyStyleNotice 应用「是否显示来源标注」。
//
// 借鉴别人的布局要写清来源，但有人不希望侧栏常驻一行小字，
// 所以给了开关；配置字段早就有，之前没接到界面上。
export function applyStyleNotice() {
  const box = $('#dsh-credit');
  if (box) box.hidden = !!S?.cfg?.ui?.hideStyleNotice;
}

// renderComposerBar 显示当前模型与权限，点一下跳到设置里的 Agent 面板。
export function renderComposerBar() {
  const bar = $('#dsh-composer-bar');
  if (!bar) return;
  const model = S?.cfg?.agent?.model || S?.cfg?.provider?.model || '跟随全局设置';
  const perm = { all: '全部访问', remind: '写操作需确认', review: '每步确认' }[S?.cfg?.agent?.permission || 'remind'];
  bar.innerHTML = '';
  const m = el('button', { class: 'dsh-pill', title: '切换模型（在设置 → Agent 里）' });
  m.innerHTML = `<i></i>${esc(model)}`;
  m.addEventListener('click', () => openSettings(S, 'agent'));
  const p = el('button', { class: 'dsh-pill' + (perm === '全部访问' ? ' warn' : ''), title: '调整 Agent 权限' });
  p.innerHTML = `<i></i>${esc(perm)}`;
  p.addEventListener('click', () => openSettings(S, 'agent'));
  bar.append(m, p);
}

async function pickStyle(style) {
  applyUiStyle(style);
  if (S?.cfg?.ui) {
    S.cfg.ui.style = style;
    try {
      const d = await api.post('/api/config', S.cfg);
      S.cfg = d.config.config;
    } catch (e) { /* 保存失败也不回滚，下次点一下即可 */ }
  }
  if (onChange) onChange(style);
}

// loadRailSessions 刷新左栏的最近课堂。
export async function loadRailSessions() {
  const box = $('#rail-sessions-list');
  if (!box) return;
  try {
    const d = await api.get('/api/sessions');
    const list = (d.sessions || []).slice(0, 12);
    const count = $('#rail-sessions-count');
    if (count) count.textContent = list.length ? String(list.length) : '';
    box.innerHTML = '';
    if (!list.length) {
      box.innerHTML = '<div class="side-empty" style="padding:6px 9px">还没有课堂记录</div>';
      return;
    }
    for (const s of list) {
      const b = el('button', { class: 'dsh-session', dataset: { id: s.id }, title: s.title || s.subject });
      b.innerHTML = `<i class="dot"></i><span class="s-main"><span class="s-title">${esc(s.title || s.subject || '未命名')}</span><span class="s-sub">${esc(fmtDate(s.startedAt))} · ${s.slides || 0} 页</span></span>`;
      if (S?.session?.id === s.id) b.classList.add('active');
      b.addEventListener('click', () => {
        if (typeof S?.openSession === 'function') S.openSession(s.id);
      });
      box.append(b);
    }
  } catch (e) {
    box.innerHTML = `<div class="side-empty" style="padding:6px 9px">${esc(e.message)}</div>`;
  }
}

// markActiveSession 高亮当前课堂。
export function markActiveSession(id) {
  document.querySelectorAll('.dsh-session').forEach((n) => {
    n.classList.toggle('active', n.dataset.id === id);
  });
}

// renderSlots 把启用的插件槽位渲染到顶栏 / 实录页。
export function renderSlots(slots) {
  document.querySelectorAll('[data-plugin-slot]').forEach((n) => n.remove());
  if (!Array.isArray(slots) || !slots.length) return;
  const statusbar = slots.filter((s) => s.slot === 'statusbar');
  const dashboard = slots.filter((s) => s.slot === 'dashboard');
  const chips = $('#status-chips');
  for (const s of statusbar) {
    const chip = el('span', { class: 'chip', dataset: { pluginSlot: 'statusbar' } });
    chip.innerHTML = `<i></i>${esc(s.title || s.pluginId)}`;
    chip.title = s.body || '';
    chips?.append(chip);
  }
  const host = $('#view-live');
  if (host && dashboard.length) {
    const wrap = el('div', { class: 'plugin-slot', dataset: { pluginSlot: 'dashboard' } });
    for (const s of dashboard) {
      const item = el('div');
      item.innerHTML = `<b>${esc(s.title || s.pluginId)}</b>${esc(s.body || '')}`;
      if (s.href) {
        const a = el('a', { href: s.href, target: '_blank', rel: 'noreferrer', text: '打开' });
        item.append(' ', a);
      }
      wrap.append(item);
    }
    host.insertBefore(wrap, host.firstChild);
  }
}
