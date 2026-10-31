import { api } from './api.js';
import { $, $$, el, toast, esc } from './util.js';
import { applyTheme } from './theme.js';

/**
 * 市场页：主题市场 + 技能包（插件）市场，独立于设置页。
 * 以全屏面板呈现，两个标签页切换。
 */
let S = null;
let tab = 'themes';

export function openMarket(state) {
  S = state;
  $('#market').hidden = false;
  bind();
  render();
}

function close() {
  $('#market').hidden = true;
}

function bind() {
  if (bind.done) return;
  bind.done = true;
  $('#market-close').addEventListener('click', close);
  $('#market-mask').addEventListener('click', close);
  $$('#market-tabs button').forEach((b) => {
    b.addEventListener('click', () => {
      tab = b.dataset.tab;
      $$('#market-tabs button').forEach((x) => x.classList.toggle('active', x === b));
      render();
    });
  });
}

function render() {
  const body = $('#market-body');
  if (tab === 'themes') {
    body.innerHTML = `
      <div class="desc">来自社区分享的主题清单，点击即可下载并应用。也可以在「设置 → 外观」里自己调色。</div>
      <div class="market-toolbar">
        <input id="mk-search" placeholder="搜索主题名称、作者…" style="flex:1;max-width:320px">
        <button class="btn small" id="mk-refresh">刷新</button>
      </div>
      <div class="theme-grid market-grid" id="mk-themes"><div class="desc">加载中…</div></div>`;
    $('#mk-refresh').addEventListener('click', loadThemes);
    $('#mk-search').addEventListener('input', () => loadThemes($('#mk-search').value));
    loadThemes();
  } else {
    body.innerHTML = `
      <div class="desc">技能包是一段可复用的提示词，启用后并入课堂助手指令。点击「安装」下载到本机，可在「设置 → Agent」管理已安装的技能包。</div>
      <div class="market-toolbar">
        <input id="mk-search" placeholder="搜索技能包名称、用途…" style="flex:1;max-width:320px">
        <button class="btn small" id="mk-refresh">刷新</button>
      </div>
      <div class="skill-market" id="mk-skills"><div class="desc">加载中…</div></div>`;
    $('#mk-refresh').addEventListener('click', loadSkills);
    $('#mk-search').addEventListener('input', () => loadSkills($('#mk-search').value));
    loadSkills();
  }
}

async function loadThemes(filter = '') {
  const box = $('#mk-themes');
  if (!box) return;
  box.innerHTML = '<div class="desc">加载中…</div>';
  try {
    const d = await api.get('/api/themes/market');
    if (!d.ok) throw new Error(d.error || '加载失败');
    const f = filter.trim().toLowerCase();
    const list = (d.themes || []).filter((t) =>
      !f || (t.name || '').toLowerCase().includes(f) || (t.author || '').toLowerCase().includes(f) || (t.note || '').toLowerCase().includes(f));
    box.innerHTML = '';
    if (!list.length) {
      box.innerHTML = `<div class="desc">${f ? '没有匹配的主题' : '市场里暂时还没有主题'}</div>`;
      return;
    }
    for (const t of list) {
      const card = document.createElement('button');
      card.className = 'theme-card market-card';
      card.innerHTML = `
        <div class="theme-swatch">
          <i style="background:${esc((t.vars || {}).bg || '#fff')}"></i>
          <i style="background:${esc(t.swatch || (t.vars || {}).accent || '#888')}"></i>
          <i style="background:${esc((t.vars || {}).line || '#ddd')}"></i>
        </div>
        <b>${esc(t.name)}</b>
        <span>${esc(t.note || '')}</span>
        <span class="muted">${esc(t.author || '社区主题')}</span>`;
      card.addEventListener('click', async () => {
        try {
          const imp = await api.post('/api/themes/import', t);
          S.cfg.ui.customThemes = imp.custom || [];
          S.cfg.ui.theme = t.id;
          applyTheme(t.id, imp.custom);
          await api.post('/api/config', S.cfg);
          toast('已应用主题：' + t.name, 'ok');
          loadThemes($('#mk-search')?.value || '');
        } catch (e) { toast(e.message, 'err'); }
      });
      box.append(card);
    }
  } catch (e) {
    box.innerHTML = '<div class="desc">' + esc(e.message) + '</div>';
  }
}

async function loadSkills(filter = '') {
  const box = $('#mk-skills');
  if (!box) return;
  box.innerHTML = '<div class="desc">加载中…</div>';
  try {
    const [mk, local] = await Promise.all([
      api.get('/api/plugins/market'),
      api.get('/api/plugins'),
    ]);
    if (!mk.ok) throw new Error(mk.error || '加载失败');
    const installed = new Set((local.skills || []).map((s) => s.id));
    const f = filter.trim().toLowerCase();
    const list = (mk.skills || []).filter((s) =>
      !f || (s.name || '').toLowerCase().includes(f) || (s.description || '').toLowerCase().includes(f) || (s.author || '').toLowerCase().includes(f));
    box.innerHTML = '';
    if (!list.length) {
      box.innerHTML = `<div class="desc">${f ? '没有匹配的技能包' : '市场里暂时还没有技能包'}</div>`;
      return;
    }
    for (const s of list) {
      const has = installed.has(s.id);
      const row = el('div', { class: 'card market-skill' });
      row.innerHTML = `
        <div class="row" style="gap:10px;align-items:center">
          <b style="flex:1">${esc(s.name)}</b>
          ${s.author ? `<span class="tag">${esc(s.author)}</span>` : ''}
          <button class="btn small ${has ? 'ghost' : 'primary'}" data-install="${esc(s.id)}" ${has ? 'disabled' : ''}>${has ? '已安装' : '安装'}</button>
        </div>
        <div class="desc" style="margin-top:6px">${esc(s.description || '')}</div>
        <div class="desc" style="margin-top:4px;opacity:.75">${esc((s.prompt || '').slice(0, 120))}</div>
        <div class="desc" style="margin-top:4px">可用工具：${esc((s.tools || []).join('、') || '无')}</div>`;
      box.append(row);
    }
    $$('#mk-skills [data-install]').forEach((b) => b.addEventListener('click', async () => {
      const item = list.find((x) => x.id === b.dataset.install);
      if (!item) return;
      b.disabled = true;
      b.textContent = '安装中…';
      try {
        await api.post('/api/plugins/install', { item });
        toast('已安装 ' + item.name, 'ok');
        loadSkills($('#mk-search')?.value || '');
      } catch (e) {
        toast(e.message, 'err');
        b.disabled = false;
        b.textContent = '安装';
      }
    }));
  } catch (e) {
    box.innerHTML = '<div class="desc">' + esc(e.message) + '</div>';
  }
}
