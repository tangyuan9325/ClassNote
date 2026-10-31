import { api } from './api.js';
import { $, $$, el, toast, esc } from './util.js';
import { applyTheme, themeLabel } from './theme.js';
import { openSettings } from './settings.js';

/**
 * 插件商城 / 主题商店。
 * 两个页面都挂在左侧导航栏，由 app.js 在切换页面时调用 render。
 */
let S = null;
let plTab = 'market';
let plFilter = '';
let thTab = 'all';
let thFilter = '';
let marketThemes = [];
let marketThemesLoaded = false;

export function initStores(state) {
  S = state;
  bindPlugins();
  bindThemes();
}

/* ================= 插件商城 ================= */

function bindPlugins() {
  if (bindPlugins.done) return;
  bindPlugins.done = true;
  $('#pl-tabs')?.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-tab]');
    if (!b) return;
    plTab = b.dataset.tab;
    $$('#pl-tabs button').forEach((x) => x.classList.toggle('active', x === b));
    renderPlugins();
  });
  $('#pl-search')?.addEventListener('input', (e) => {
    plFilter = e.target.value;
    renderPlugins();
  });
  $('#pl-refresh')?.addEventListener('click', () => renderPlugins(true));
}

export async function renderPlugins(force = false) {
  const box = $('#pl-list');
  if (!box) return;
  box.innerHTML = '<div class="side-empty">加载中…</div>';
  let market = [];
  let local = [];
  try {
    const [mk, lc] = await Promise.all([
      api.get('/api/plugins/market').catch((e) => ({ ok: false, error: e.message, skills: [] })),
      api.get('/api/plugins'),
    ]);
    market = mk.skills || [];
    local = lc.skills || [];
    if (force && !mk.ok) toast('市场清单拉取失败，已显示本地内容', 'err');
  } catch (e) {
    box.innerHTML = `<div class="store-empty">${esc(e.message)}</div>`;
    return;
  }
  updateCount(local.length);
  const installed = new Set(local.map((s) => s.id));
  const f = plFilter.trim().toLowerCase();
  const match = (s) => !f || [s.name, s.description, s.author, s.id].join(' ').toLowerCase().includes(f);

  if (plTab === 'installed') {
    const list = local.filter(match);
    box.innerHTML = '';
    if (!list.length) {
      box.innerHTML = `<div class="store-empty">${f ? '没有匹配的插件' : '还没有安装插件，去「市场」看看'}</div>`;
      return;
    }
    for (const s of list) box.append(installedRow(s));
    return;
  }

  const list = market.filter(match);
  box.innerHTML = '';
  if (!list.length) {
    box.innerHTML = `<div class="store-empty">${f ? '没有匹配的插件' : '市场里暂时没有插件'}</div>`;
    return;
  }
  for (const s of list) box.append(marketRow(s, installed.has(s.id)));
}

function updateCount(n) {
  const elCount = $('#pl-count');
  if (elCount) elCount.textContent = String(n);
}

function iconText(name) {
  return (name || '?').trim().slice(0, 1);
}

function marketRow(s, has) {
  const row = el('div', { class: 'store-row' });
  row.innerHTML = `
    <div class="store-icon">${esc(iconText(s.name))}</div>
    <div class="store-main">
      <b>${esc(s.name)} ${s.builtIn ? '<span class="tag">内置</span>' : ''}</b>
      <p>${esc(s.description || '没有描述')}</p>
    </div>
    <div class="store-meta">
      <span>↓ ${fmtNum(s.downloads)}</span>
      <span class="star">★ ${fmtNum(s.stars)}</span>
    </div>
    <div class="store-actions">
      <button class="btn small ${has ? 'ghost' : 'primary'}" ${has ? 'disabled' : ''}>${has ? '已安装' : '安装'}</button>
    </div>`;
  const btn = row.querySelector('button');
  if (!has) {
    btn.addEventListener('click', async () => {
      btn.disabled = true;
      btn.textContent = '安装中…';
      try {
        await api.post('/api/plugins/install', { item: s });
        toast('已安装 ' + s.name, 'ok');
        renderPlugins();
      } catch (e) {
        toast(e.message, 'err');
        btn.disabled = false;
        btn.textContent = '安装';
      }
    });
  }
  return row;
}

function installedRow(s) {
  const row = el('div', { class: 'store-row' });
  row.innerHTML = `
    <div class="store-icon alt">${esc(iconText(s.name))}</div>
    <div class="store-main">
      <b>${esc(s.name)} ${s.builtIn ? '<span class="tag">内置</span>' : ''}</b>
      <p>${esc(s.description || '没有描述')}</p>
    </div>
    <div class="store-actions">
      <label class="switch" title="${s.enabled ? '已启用' : '已停用'}">
        <input type="checkbox" ${s.enabled ? 'checked' : ''}><i></i>
      </label>
      <button class="btn small ghost" data-act="edit">编辑</button>
      <button class="btn small danger" data-act="del">删除</button>
    </div>`;
  row.querySelector('input').addEventListener('change', async (e) => {
    try {
      await api.post('/api/plugins/toggle', { id: s.id, enabled: e.target.checked });
      s.enabled = e.target.checked;
      toast((s.enabled ? '已启用 ' : '已停用 ') + s.name, 'ok');
    } catch (err) {
      e.target.checked = !e.target.checked;
      toast(err.message, 'err');
    }
  });
  row.querySelector('[data-act="del"]').addEventListener('click', async () => {
    if (!window.confirm(`删除技能包「${s.name}」？`)) return;
    try {
      await api.post('/api/plugins/delete', { id: s.id });
      toast('已删除', 'ok');
      renderPlugins();
    } catch (err) { toast(err.message, 'err'); }
  });
  row.querySelector('[data-act="edit"]').addEventListener('click', () => toggleEditor(row, s));
  return row;
}

function toggleEditor(row, s) {
  const next = row.nextElementSibling;
  if (next && next.classList.contains('skill-edit')) {
    next.remove();
    return;
  }
  const box = el('div', { class: 'skill-edit' });
  box.innerHTML = `
    <div class="desc" style="margin:0 0 8px">编辑提示词，启用后会并入课堂助手的系统指令。</div>
    <textarea></textarea>
    <div class="row" style="gap:8px;margin-top:8px">
      <button class="btn small primary" data-act="save">保存</button>
      <button class="btn small ghost" data-act="cancel">取消</button>
    </div>`;
  const ta = box.querySelector('textarea');
  ta.value = s.prompt || '';
  box.querySelector('[data-act="cancel"]').addEventListener('click', () => box.remove());
  box.querySelector('[data-act="save"]').addEventListener('click', async () => {
    try {
      await api.post('/api/plugins/save', { ...s, prompt: ta.value, enabled: s.enabled });
      toast('提示词已保存', 'ok');
      box.remove();
    } catch (err) { toast(err.message, 'err'); }
  });
  row.after(box);
  ta.focus();
}

function fmtNum(n) {
  n = Number(n) || 0;
  if (n >= 10000) return (n / 10000).toFixed(1) + 'w';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}

/* ================= 主题商店 ================= */

function bindThemes() {
  if (bindThemes.done) return;
  bindThemes.done = true;
  $('#th-tabs')?.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-tab]');
    if (!b) return;
    thTab = b.dataset.tab;
    $$('#th-tabs button').forEach((x) => x.classList.toggle('active', x === b));
    renderThemes();
  });
  $('#th-search')?.addEventListener('input', (e) => {
    thFilter = e.target.value;
    renderThemes();
  });
  $('#th-refresh')?.addEventListener('click', () => {
    marketThemesLoaded = false;
    renderThemes(true);
  });
  $('#th-new')?.addEventListener('click', () => openSettings(S, 'appearance'));
  $('#th-import')?.addEventListener('click', () => $('#th-file')?.click());
  $('#th-file')?.addEventListener('change', async (e) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    try {
      const data = JSON.parse(await file.text());
      const d = await api.post('/api/themes/import', data);
      S.cfg.ui.customThemes = d.custom || S.cfg.ui.customThemes;
      toast('已导入 ' + (d.theme?.name || '主题'), 'ok');
      renderThemes();
    } catch (err) { toast(err.message, 'err'); }
  });
}

export async function renderThemes(force = false) {
  const box = $('#th-list');
  if (!box) return;
  box.innerHTML = '<div class="side-empty">加载中…</div>';
  if ((!marketThemesLoaded || force) && thTab !== 'mine') {
    try {
      const d = await api.get('/api/themes/market');
      marketThemes = d.themes || [];
    } catch (e) {
      marketThemes = [];
    }
    marketThemesLoaded = true;
  }
  const builtins = (S.boot?.themes || []).map((t) => ({ ...t, kind: 'builtin' }));
  const customs = (S.cfg?.ui?.customThemes || []).map((t) => ({ ...t, kind: 'custom' }));
  const f = thFilter.trim().toLowerCase();
  const match = (t) => !f || [t.name, t.note, t.author, t.id].join(' ').toLowerCase().includes(f);

  let sections = [];
  if (thTab === 'all') {
    sections = [
      ['内置主题', builtins.filter(match), 'builtin'],
      ['我的主题', customs.filter(match), 'custom'],
      ['社区分享', marketThemes.filter(match), 'market'],
    ];
  } else if (thTab === 'mine') {
    sections = [
      ['内置主题', builtins.filter(match), 'builtin'],
      ['我的主题', customs.filter(match), 'custom'],
    ];
  } else {
    sections = [['社区分享', marketThemes.filter(match), 'market']];
  }

  box.innerHTML = '';
  let total = 0;
  for (const [title, list, kind] of sections) {
    const sec = el('div', { class: 'theme-section' });
    sec.innerHTML = `<h3>${title}</h3><div class="theme-store-grid"></div>`;
    const grid = sec.querySelector('.theme-store-grid');
    if (!list.length) {
      grid.innerHTML = `<div class="store-empty">${kind === 'market' ? '社区暂时没有主题' : '还没有主题'}</div>`;
    }
    for (const t of list) grid.append(themeTile(t, kind));
    total += list.length;
    box.append(sec);
  }
  if (!total) box.innerHTML = '<div class="store-empty">没有匹配的主题</div>';
}

function themeTile(t, kind) {
  const vars = t.vars || {};
  const bg = vars.bg || (t.id === 'graphite' ? '#1a1a19' : t.id === 'mist' ? '#f4f6f8' : t.id === 'forest' ? '#141815' : '#faf9f5');
  const accent = t.swatch || vars.accent || '#c96442';
  const line = vars.line || (t.id === 'graphite' ? '#312e2a' : t.id === 'mist' ? '#dde4ea' : t.id === 'forest' ? '#2a332c' : '#e6e1d5');
  const active = S.cfg?.ui?.theme === t.id;
  const tile = el('div', { class: 'theme-tile' + (active ? ' active' : '') });
  tile.innerHTML = `
    <div class="theme-swatch"><i style="background:${esc(bg)}"></i><i style="background:${esc(accent)}"></i><i style="background:${esc(line)}"></i></div>
    <div class="t-name"><span>${esc(t.name || t.id)}</span><span class="t-badge">${active ? '使用中' : kind === 'builtin' ? '内置' : kind === 'custom' ? '自定义' : '社区'}</span></div>
    <span class="t-note">${esc(t.note || t.author || '')}</span>
    <div class="t-actions"></div>`;
  const actions = tile.querySelector('.t-actions');
  if (kind === 'market') {
    actions.append(btnMini('安装并应用', 'primary', () => installTheme(t)));
  } else {
    if (!active) actions.append(btnMini('应用', 'primary', () => applyThemeNow(t.id)));
    else actions.append(btnMini('已应用', 'ghost', null, true));
    if (kind === 'custom') {
      actions.append(btnMini('导出', '', () => exportTheme(t)));
      actions.append(btnMini('删除', 'danger', () => deleteTheme(t)));
    }
  }
  return tile;
}

function btnMini(text, cls, fn, disabled) {
  const b = el('button', { class: 'btn small ' + cls, text });
  if (disabled) b.disabled = true;
  if (fn) b.addEventListener('click', fn);
  return b;
}

async function applyThemeNow(id) {
  S.cfg.ui.theme = id;
  applyTheme(id, S.cfg.ui.customThemes);
  S.themeIndex = Math.max(0, (S.boot?.themes || []).findIndex((t) => t.id === id));
  const label = $('#theme-name');
  if (label) label.textContent = themeLabel(id, S.boot?.themes, S.cfg.ui.customThemes);
  try {
    const d = await api.post('/api/config', S.cfg);
    S.cfg = d.config.config;
    toast('已应用 ' + themeLabel(id, S.boot?.themes, S.cfg.ui.customThemes), 'ok');
  } catch (e) { toast(e.message, 'err'); }
  renderThemes();
}

async function installTheme(t) {
  try {
    const d = await api.post('/api/themes/import', t);
    S.cfg.ui.customThemes = d.custom || S.cfg.ui.customThemes;
    await applyThemeNow(t.id);
    toast('已安装并应用 ' + t.name, 'ok');
  } catch (e) { toast(e.message, 'err'); }
}

async function deleteTheme(t) {
  if (!window.confirm(`删除主题「${t.name}」？`)) return;
  try {
    const d = await api.post('/api/themes/delete', { id: t.id });
    S.cfg.ui.customThemes = d.custom || (S.cfg.ui.customThemes || []).filter((x) => x.id !== t.id);
    if (S.cfg.ui.theme === t.id) {
      S.cfg.ui.theme = 'claude';
      applyTheme('claude', S.cfg.ui.customThemes);
    }
    await api.post('/api/config', S.cfg);
    toast('已删除', 'ok');
    renderThemes();
  } catch (e) { toast(e.message, 'err'); }
}

function exportTheme(t) {
  const blob = new Blob([JSON.stringify({ format: 'classnote-theme', version: 1, theme: { id: t.id, name: t.name, vars: t.vars } }, null, 2)], { type: 'application/json' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'classnote-theme-' + t.id + '.json';
  a.click();
  URL.revokeObjectURL(a.href);
}
