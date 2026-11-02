import { api, connectEvents } from './api.js';
import { applyTheme as applyThemeVars, themeLabel } from './theme.js';
import { $, $$, el, toast, esc, md, fmtTime, fmtDate, fmtDur } from './util.js';
import { startOnboarding } from './onboarding.js';
import { openSettings } from './settings.js';
import { initStores, renderPlugins, renderThemes } from './store.js';
import { applyUiStyle, mountShell, loadRailSessions, markActiveSession, renderSlots, renderComposerBar } from './shell.js';
import { initAgent, loadAgentInfo, refreshAgentContext, renderAgentPage as renderAgentPageImpl, askAgent as askAgentImpl, agentEvent } from './agent.js';

export const S = {
  boot: null,
  cfg: null,
  status: null,
  session: null,
  view: 'live',
  page: 'home',
  live: false,
  cards: new Map(),
  themeIndex: 0,
  asking: false,
  agentAsking: false,
  agentMsgs: [],
  timers: [],
};

const VIEW_TITLE = { live: '课堂实录', history: '历史课堂', timetable: '今日课表' };
const PAGE_TITLE = { home: '课堂实录', agent: 'Agent 对话', plugins: '插件商城', themes: '主题商店' };

/* ---------------- 启动 ---------------- */

async function init() {
  try {
    S.boot = await api.get('/api/bootstrap');
  } catch (e) {
    document.body.innerHTML = `<div style="padding:60px;font-family:sans-serif">无法连接本地服务：${esc(e.message)}</div>`;
    return;
  }
  S.cfg = S.boot.config.config;
  S.themeIndex = Math.max(0, (S.boot.themes || []).findIndex((t) => t.id === S.cfg.ui.theme));
  applyTheme(S.cfg.ui.theme);
  applyUiStyle(S.cfg.ui?.style);

  bindGlobal();

  if (!S.boot.app.onboarded) {
    startOnboarding({
      boot: S.boot,
      onDone: async () => { await reloadBoot(); enterApp(); },
    });
  } else {
    enterApp();
  }
}

async function reloadBoot() {
  S.boot = await api.get('/api/bootstrap');
  S.cfg = S.boot.config.config;
}

function enterApp() {
  $('#onboarding').hidden = true;
  $('#app').hidden = false;
  initStores(S);
  applyUiStyle(S.cfg?.ui?.style);
  applyRailState(!!S.cfg?.ui?.sidebarCollapsed);
  mountShell(S, { onStyleChange: (style) => { S.cfg.ui.style = style; refreshShell(); } });
  initAgent(S, { refreshSession: () => refreshLiveSession() });
  loadAgentInfo();
  loadSlots();
  connectEvents(handlers);
  refreshStatus();
  loadSessions();
  // 托盘“设置…”会以 ?panel=settings 打开窗口。
  const panel = new URLSearchParams(location.search).get('panel');
  if (panel === 'settings') {
    setTimeout(() => openSettings(S), 400);
    history.replaceState(null, '', location.pathname);
  }
  S.timers.push(setInterval(refreshStatus, 2000));
  S.timers.push(setInterval(() => { if (S.live) refreshLiveSession(); }, 6000));
}

/* ---------------- 左侧导航 ---------------- */

function applyRailState(collapsed) {
  const app = $('#app');
  app.classList.toggle('rail-collapsed', collapsed);
  const btn = $('#rail-toggle');
  if (btn) {
    btn.title = collapsed ? '展开侧边栏' : '收拢侧边栏';
    btn.setAttribute('aria-expanded', String(!collapsed));
  }
}

function toggleRail() {
  const collapsed = !$('#app').classList.contains('rail-collapsed');
  applyRailState(collapsed);
  S.cfg.ui.sidebarCollapsed = collapsed;
  saveConfig(true);
}

function setPage(page) {
  S.page = page;
  $('#app').dataset.page = page;
  $$('.rail-item[data-page], .rail-group[data-page]').forEach((b) => b.classList.toggle('active', b.dataset.page === page));
  $$('.view').forEach((sec) => { sec.hidden = sec.id !== 'view-' + (page === 'home' ? S.view : page); });
  $('#page-title').textContent = page === 'home' ? (VIEW_TITLE[S.view] || '课堂实录') : (PAGE_TITLE[page] || 'ClassNote');
  if (page === 'agent') renderAgentPage();
  if (page === 'plugins') renderPlugins();
  if (page === 'themes') renderThemes();
}

/* ---------------- 主题 ---------------- */

function applyTheme(name) {
  const label = applyThemeVars(name, S?.cfg?.ui?.customThemes);
  $('#theme-name').textContent = themeLabel(name, S?.boot?.themes, S?.cfg?.ui?.customThemes) || label;
}

function cycleTheme() {
  const list = S.boot?.themes || [];
  if (!list.length) return;
  S.themeIndex = (S.themeIndex + 1) % list.length;
  const t = list[S.themeIndex];
  applyTheme(t.id);
  S.cfg.ui.theme = t.id;
  saveConfig(true);
  toast(`主题：${t.name}`);
}

/* ---------------- 状态 ---------------- */

async function refreshStatus() {
  try {
    const d = await api.get('/api/status');
    S.status = d;
    renderStatus(d);
    const todayKey = (d.classIsland?.today || []).map((x) => x.start + x.subject).join('|');
    if (todayKey !== S.todayKey) {
      S.todayKey = todayKey;
      renderToday(d.classIsland);
    }
  } catch (e) { /* 服务重启中忽略 */ }
}

function renderStatus(d) {
  const st = d.status || {};
  const ci = d.classIsland || {};
  const chipClass = $('#chip-class');
  const chipSlide = $('#chip-slide');
  const chipRec = $('#chip-rec');

  if (ci.inClass && ci.subject) {
    chipClass.className = 'chip on';
    chipClass.innerHTML = `<i></i>上课中 · ${esc(ci.subject)}`;
  } else if (ci.preparing && ci.next) {
    chipClass.className = 'chip';
    chipClass.innerHTML = `<i></i>即将上课 · ${esc(ci.next)} ${esc(ci.nextStart || '')}`;
  } else if (ci.next) {
    chipClass.className = 'chip';
    chipClass.innerHTML = `<i></i>下节 ${esc(ci.next)} ${esc(ci.nextStart || '')}`;
  } else {
    chipClass.className = 'chip';
    chipClass.innerHTML = `<i></i>${ci.running ? '非上课时间' : '课表未连接'}`;
  }

  const slide = st.slide || {};
  if (slide.presenting) {
    chipSlide.className = 'chip on';
    const idx = slide.index > 0 ? `第 ${slide.index} 页` : '放映中';
    chipSlide.innerHTML = `<i></i>${esc(slide.app || '放映')} · ${idx}`;
  } else {
    chipSlide.className = 'chip';
    chipSlide.innerHTML = '<i></i>未放映';
  }

  if (st.recording) {
    chipRec.className = 'chip on';
    chipRec.innerHTML = `<i></i>录音中 ${st.speaking ? '· 讲话' : ''}`;
  } else if (st.audioError) {
    chipRec.className = 'chip';
    chipRec.innerHTML = '<i></i>麦克风异常';
  } else {
    chipRec.className = 'chip';
    chipRec.innerHTML = '<i></i>未录音';
  }

  const dot = $('#live-dot');
  if (dot) dot.className = 'live-dot' + (st.recording && st.speaking ? ' on' : '');

  const wave = $('#wave');
  if (wave) wave.classList.toggle('quiet', !(st.recording && st.level > 0.012));

  // 顶栏按钮
  const running = !!st.running;
  const btn = $('#btn-toggle-lesson');
  btn.classList.toggle('primary', !running);
  $('#btn-toggle-text').textContent = running ? '结束记录' : '开始记录';
  $('#btn-capture').disabled = !running;
  $('#btn-report').disabled = !running && !S.session;

  // 概览：正在记录时跟随引擎状态，查看历史课堂时保持会话统计
  if (running) {
    const words = st.words || 0;
    $('#stat-pages').textContent = st.segments || 0;
    $('#stat-words').textContent = words > 999 ? (words / 1000).toFixed(1) + 'k' : words;
    $('#stat-time').textContent = fmtDur(st.elapsedSec || 0);
  }

  const hero = $('#live-hero');
  if (running && !S.session && st.sessionId) {
    // 首次进入时补拉当前会话
    refreshLiveSession();
  }
  hero.hidden = !running;
  if (running) {
    $('#live-title').textContent = st.title || st.subject || '课堂记录';
    const parts = [];
    if (st.segments) parts.push(`已记录 ${st.segments} 个片段`);
    if (words) parts.push(`${words} 字`);
    if (st.slide?.presenting) parts.push('正在放映');
    else if (st.recording) parts.push('录音中');
    $('#live-sub').textContent = parts.join(' · ') || '等待幻灯片与语音…';
  }
  const emptyEl = $('#empty-state');
  if (emptyEl) emptyEl.style.display = running || (S.session && S.session.segments?.length) ? 'none' : '';
  if (S.page === 'agent') refreshAgentContext();
}

async function refreshLiveSession() {
  try {
    const d = await api.get('/api/session');
    if (!d.session) return;
    const same = S.session && S.session.id === d.session.id;
    S.session = d.session;
    S.live = !!d.live;
    renderTimeline(d.session, !same);
    renderInspector(d.session);
  } catch (e) { /* ignore */ }
}

/* ---------------- 会话 / 时间线 ---------------- */

function renderTimeline(session, rebuild = true) {
  const wrap = $('#timeline');
  if (rebuild) {
    wrap.innerHTML = '';
    S.cards.clear();
  }
  const segs = (session.segments || []).filter((s) => s.imageFile || s.summary || s.notes || (s.transcript || '').length > 20);
  if (!segs.length) {
    if (!S.status?.status?.running) wrap.append(emptyState());
    return;
  }
  for (const seg of segs) upsertCard(seg, rebuild);
  renderReportCard(session);
  if (rebuild) wrap.scrollTop = wrap.scrollHeight;
}

function emptyState() {
  return el('div', { class: 'empty-state', id: 'empty-state' }, [
    el('div', { class: 'empty-art', html: `<svg viewBox="0 0 96 96" fill="none"><rect x="10" y="18" width="76" height="54" rx="7" stroke="currentColor" stroke-width="2"/><path d="M24 34h32M24 45h22M24 56h15" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><circle cx="70" cy="52" r="10" stroke="currentColor" stroke-width="2"/><path d="M70 47v5l3.5 2" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>` }),
    el('h3', { text: '还没有开始记录' }),
    el('p', { text: '当 ClassIsland 显示上课时，会自动开始；也可以点击左侧「开始记录」。' }),
    el('div', { class: 'empty-actions' }, [
      el('button', { class: 'btn primary', onclick: toggleLesson }, '立即开始'),
      el('button', { class: 'btn', onclick: () => openSettings(S) }, '检查集成'),
    ]),
  ]);
}

function upsertCard(seg, forceNew = false) {
  const wrap = $('#timeline');
  let card = S.cards.get(seg.id);
  if (!card || forceNew) {
    if (!card) {
      const empty = $('#empty-state');
      if (empty) empty.remove();
    }
    card = el('article', { class: 'card', dataset: { id: seg.id } });
    S.cards.set(seg.id, card);
    wrap.append(card);
  }
  renderCard(card, seg);
  return card;
}

function renderReportCard(session) {
  const wrap = $('#timeline');
  const old = $('#report-card');
  if (old) old.remove();
  const rep = session.report;
  if (!rep) return;
  const card = el('article', { class: 'card', id: 'report-card' });
  card.innerHTML = `
    <div class="card-top">
      <span class="card-no">课堂报告</span>
      <span class="card-time">${fmtTime(rep.createdAt)}</span>
    </div>
    <p class="card-summary">${esc(rep.title || session.title)}</p>
    <div class="card-body"><p>${esc(rep.summary || '')}</p></div>
    <div class="card-foot">
      <span class="copy-badge ${rep.copyRate >= 60 ? 'high' : rep.copyRate >= 40 ? 'mid' : ''}"><i></i>照搬度 ${rep.copyRate}%</span>
      <span style="margin-left:auto;display:flex;gap:6px">
        ${rep.pdfPath ? '<button class="btn small" data-open="pdf">打开 PDF</button>' : ''}
        <button class="btn small" data-open="html">打开 HTML</button>
      </span>
    </div>`;
  card.querySelectorAll('[data-open]').forEach((b) => b.addEventListener('click', () => {
    window.open(`/api/report-file/${session.id}?type=${b.dataset.open}`, '_blank');
  }));
  wrap.append(card);
}

function imageURL(seg) {
  if (!seg.imageFile) return '';
  const sid = S.session?.id;
  if (!sid) return '';
  if (seg.imageFile.includes('/')) return '/api/media/' + seg.imageFile;
  return `/api/media/${sid}/${seg.imageFile}`;
}

function renderCard(card, seg) {
  const img = imageURL(seg);
  const scored = typeof seg.copyScore === 'number' && seg.copyScore >= 0;
  const copyClass = !scored ? 'unknown' : seg.copyScore >= 70 ? 'high' : seg.copyScore >= 40 ? 'mid' : '';
  const copyText = seg.status === 'done'
    ? (!scored ? '未判定' : seg.copyScore >= 70 ? '疑似照搬' : seg.copyScore >= 40 ? '部分照搬' : '讲解为主')
    : '';

  let foot = '';
  if (seg.status === 'analyzing') foot = '<span class="analyzing">AI 正在整理本页</span>';
  else if (seg.status === 'pending') foot = '<span class="hint">排队等待整理…</span>';
  else if (seg.status === 'failed') foot = `<span class="card-error">整理失败：${esc(seg.error || '未知错误')}</span>`;
  else if (seg.status === 'skipped') foot = '<span class="hint">未启用 AI 整理</span>';
  else if (seg.status === 'recording') foot = '<span class="analyzing">记录中，翻页后自动整理</span>';
  else if (copyText) foot = `<span class="copy-badge ${copyClass}"><i></i>${copyText}</span>`;

  card.innerHTML = `
    <div class="card-top">
      <span class="card-no">PAGE ${String(seg.index).padStart(2, '0')}${seg.slideIndex && seg.slideIndex !== seg.index ? ` · 幻灯片 ${seg.slideIndex}` : ''}</span>
      <span class="card-time">${fmtTime(seg.createdAt)}${seg.durationSec ? ' · ' + fmtDur(seg.durationSec) : ''}</span>
    </div>
    ${img ? `<img class="card-shot" src="${img}" alt="第 ${seg.index} 页" loading="lazy">` : ''}
    ${seg.summary ? `<p class="card-summary">${esc(seg.summary)}</p>` : ''}
    ${seg.notes ? `<div class="card-body">${md(seg.notes)}</div>` : (seg.status === 'analyzing' ? '<div class="card-skeleton" style="width:88%"></div><div class="card-skeleton" style="width:66%"></div>' : '')}
    ${seg.keyPoints?.length ? `<div class="points">${seg.keyPoints.map((p) => `<span class="point">${esc(p)}</span>`).join('')}</div>` : ''}
    ${seg.tips?.length ? `<div class="card-note">提示：${seg.tips.map(esc).join(' · ')}</div>` : ''}
    <div class="card-foot">
      ${foot}
      ${seg.copyEvidence ? `<span>·</span><span>${esc(seg.copyEvidence)}</span>` : ''}
      ${seg.transcript ? `<span style="margin-left:auto">${esc(seg.transcript.slice(0, 0))}</span>` : ''}
    </div>`;

  const shot = card.querySelector('.card-shot');
  if (shot) shot.addEventListener('click', () => openLightbox(shot.src));
}

/* ---------------- 右侧检查器 ---------------- */

function renderInspector(session) {
  const segs = (session.segments || []).filter((s) => s.imageFile || s.summary || s.notes || (s.transcript || '').length > 20);
  const last = segs[segs.length - 1];
  const img = $('#slide-img');
  const emptyBox = $('#slide-frame .slide-empty');
  if (last && last.imageFile) {
    img.src = imageURL(last);
    img.hidden = false;
    if (emptyBox) emptyBox.style.display = 'none';
  } else if (!S.live) {
    img.hidden = true;
    if (emptyBox) emptyBox.style.display = '';
  }
  if (last) $('#transcript').textContent = last.transcript || '等待语音…';
  renderStats(session);
  renderChat(session);
}

function renderStats(session) {
  const segs = (session.segments || []).filter((s) => s.imageFile || s.summary || s.notes || (s.transcript || '').length > 20);
  const words = segs.reduce((a, s) => a + (s.transcript || '').length, 0);
  const start = new Date(session.startedAt).getTime();
  const end = session.endedAt ? new Date(session.endedAt).getTime() : Date.now();
  $('#stat-pages').textContent = segs.length;
  $('#stat-words').textContent = words > 999 ? (words / 1000).toFixed(1) + 'k' : words;
  $('#stat-time').textContent = fmtDur((end - start) / 1000);
  const scored = segs.filter((s) => s.status === 'done' && s.copyScore >= 0);
  if (scored.length) {
    const avg = Math.round(scored.reduce((a, s) => a + s.copyScore, 0) / scored.length);
    $('#stat-copy').textContent = avg + '%';
  } else {
    $('#stat-copy').textContent = '–';
  }
}

function renderChat(session) {
  const box = $('#insp-chat');
  const msgs = session.messages || [];
  if (!msgs.length) { box.innerHTML = '<div class="side-empty">还没有提问</div>'; }
  else {
    box.innerHTML = '';
    for (let i = 0; i < msgs.length; i += 2) {
      const q = msgs[i];
      const a = msgs[i + 1];
      if (!q) continue;
      const qa = el('div', { class: 'qa' });
      qa.append(el('div', { class: 'q', text: q.content }));
      if (a) qa.append(el('div', { class: 'a', html: md(a.content) }));
      box.append(qa);
    }
  }
  // 主页面底部 Agent 对话框同步历史消息
  renderAgentThread(session);
}

// renderAgentThread 把本课已保存的 Agent 对话渲染到底部对话框。
// ask() 过程中的临时气泡以 [data-tmp] 标记，重建时保留。
function renderAgentThread(session) {
  const thread = $('#chat-thread');
  if (!thread) return;
  const saved = [];
  for (const n of thread.children) {
    if (n.dataset.tmp === '1') saved.push(n);
  }
  thread.innerHTML = '';
  const msgs = session.messages || [];
  for (let i = 0; i < msgs.length; i += 2) {
    const q = msgs[i];
    const a = msgs[i + 1];
    if (!q) continue;
    const qb = el('div', { class: 'bubble user', text: q.content });
    thread.append(qb);
    if (a) thread.append(el('div', { class: 'bubble assistant', html: md(a.content) }));
  }
  for (const n of saved) thread.append(n);
  thread.scrollTop = thread.scrollHeight;
}

function renderSlideEvent(data) {
  const img = $('#slide-img');
  const src = data.image ? '/api/media/' + data.image : '';
  if (src) { img.src = src + '?t=' + Date.now(); img.hidden = false; }
  const emptyBox = $('#slide-frame .slide-empty');
  if (emptyBox) emptyBox.style.display = 'none';
}

/* ---------------- 操作 ---------------- */

async function toggleLesson() {
  const running = S.status?.status?.running;
  try {
    if (running) {
      toast('正在结束并整理…');
      const d = await api.post('/api/lesson/stop', { report: false });
      S.session = d.session;
      S.live = false;
      toast('已结束记录', 'ok');
      refreshStatus();
      loadSessions();
      renderTimeline(S.session);
      renderInspector(S.session);
    } else {
      const subject = S.status?.classIsland?.subject || '课堂';
      await api.post('/api/lesson/start', { subject, source: 'manual' });
      S.session = null;
      S.cards.clear();
      $('#timeline').innerHTML = '';
      toast(`开始记录：${subject}`, 'ok');
      await refreshStatus();
      await refreshLiveSession();
    }
  } catch (e) {
    toast(e.message, 'err');
  }
}

async function captureNow() {
  try {
    await api.post('/api/lesson/capture');
    toast('已截取当前画面', 'ok');
  } catch (e) {
    toast(e.message, 'err');
  }
}

async function makeReport() {
  const btn = $('#btn-report');
  btn.disabled = true;
  const old = btn.innerHTML;
  btn.textContent = '生成中…';
  try {
    const body = S.session && !S.live ? { sessionId: S.session.id } : {};
    const d = await api.post('/api/lesson/report', body);
    const sid = d.session?.id || S.session?.id;
    toast('报告已生成', 'ok');
    if (sid) window.open(`/api/report-file/${sid}?type=pdf`, '_blank');
  } catch (e) {
    toast(e.message, 'err');
  } finally {
    btn.disabled = false;
    btn.innerHTML = old;
  }
}

async function ask() {
  const input = $('#ask-input');
  const q = input.value.trim();
  if (!q || S.asking) return;
  S.asking = true;
  $('#btn-ask').disabled = true;
  input.value = '';
  input.style.height = 'auto';
  addBubble('user', q);
  const thinking = addBubble('assistant', '正在结合本课内容思考…');
  try {
    const d = await api.post('/api/lesson/ask', { question: q });
    thinking.innerHTML = md(d.answer);
    // 回答已入库，清掉临时气泡后整体重建（避免与持久化消息重复）
    $$('#chat-thread [data-tmp]').forEach((n) => n.remove());
    refreshLiveSession();
  } catch (e) {
    thinking.innerHTML = `<span style="color:var(--danger)">${esc(e.message)}</span>`;
  } finally {
    S.asking = false;
    $('#btn-ask').disabled = false;
    input.focus();
  }
}

function addBubble(role, text) {
  const thread = $('#chat-thread');
  const b = el('div', { class: 'bubble ' + role, dataset: { tmp: '1' } });
  if (role === 'assistant') b.innerHTML = md(text); else b.textContent = text;
  thread.append(b);
  thread.scrollTop = thread.scrollHeight;
  return b;
}

function openLightbox(src) {
  $('#lightbox-img').src = src;
  $('#lightbox').hidden = false;
}

/* ---------------- 历史 / 课表 ---------------- */

async function loadSessions() {
  try {
    const d = await api.get('/api/sessions');
    const list = $('#session-list');
    const items = d.sessions || [];
    if (list) {
      if (!items.length) {
        list.innerHTML = '<div class="side-empty">还没有课堂记录</div>';
      } else {
        list.innerHTML = '';
        for (const s of items.slice(0, 40)) {
          const item = el('div', { class: 'session-item', dataset: { id: s.id } }, [
            el('div', { class: 's-top' }, [
              el('span', { class: 's-subj', text: s.title || s.subject }),
              el('span', { class: 's-date', text: fmtDate(s.startedAt) }),
            ]),
            el('div', { class: 's-meta', text: `${s.slides || 0} 片段 · ${s.words || 0} 字${s.hasReport ? ' · 有报告' : ''}` }),
          ]);
          item.addEventListener('click', () => openSession(s.id));
          if (S.session?.id === s.id) item.classList.add('active');
          list.append(item);
        }
      }
    }
    renderHistory(items);
    loadRailSessions();
  } catch (e) { /* ignore */ }
}

function renderHistory(items) {
  const grid = $('#history-grid');
  grid.innerHTML = '';
  if (!items.length) { grid.innerHTML = '<div class="side-empty">还没有课堂记录</div>'; return; }
  for (const s of items) {
    const card = el('div', { class: 'hist-card' }, [
      el('h3', { text: s.title || s.subject }),
      el('div', { class: 'h-meta', text: `${fmtDate(s.startedAt)} · ${s.slides || 0} 片段 · ${s.words || 0} 字` }),
      el('div', { class: 'h-foot', text: s.hasReport ? '查看报告 →' : '查看记录 →' }),
    ]);
    card.addEventListener('click', () => openSession(s.id));
    // Agent 对话记录：默认折叠，点击展开
    const chatBox = el('div', { class: 'hist-chat' });
    const toggle = el('button', { class: 'hist-chat-toggle' }, '▸ Agent 对话记录');
    const list = el('div', { class: 'hist-chat-list', hidden: true });
    chatBox.append(toggle, list);
    toggle.addEventListener('click', async (e) => {
      e.stopPropagation();
      if (!list.hidden) { list.hidden = true; toggle.textContent = '▸ Agent 对话记录'; return; }
      toggle.textContent = '▾ Agent 对话记录';
      list.hidden = false;
      if (!list.dataset.loaded) {
        list.innerHTML = '<div class="side-empty">加载中…</div>';
        try {
          const d = await api.get(`/api/session?id=${encodeURIComponent(s.id)}`);
          const msgs = d.session?.messages || [];
          list.innerHTML = '';
          if (!msgs.length) {
            list.innerHTML = '<div class="side-empty">这节课没有对话</div>';
          } else {
            for (let i = 0; i < msgs.length; i += 2) {
              const q = msgs[i], a = msgs[i + 1];
              if (q) list.append(el('div', { class: 'q', text: q.content }));
              if (a) list.append(el('div', { class: 'a', html: md(a.content) }));
            }
          }
          list.dataset.loaded = '1';
        } catch (err) {
          list.innerHTML = `<div class="side-empty">${esc(err.message)}</div>`;
        }
      }
    });
    card.append(chatBox);
    grid.append(card);
  }
}

async function openSession(id) {
  try {
    const d = await api.get(`/api/session?id=${encodeURIComponent(id)}`);
    if (!d.session) return;
    S.session = d.session;
    S.live = !!d.live;
    markActiveSession(id);
    setView('live');
    renderTimeline(d.session);
    renderInspector(d.session);
    $('#page-title').textContent = d.session.title || '课堂记录';
    $('#live-hero').hidden = true;
    const emptyEl = $('#empty-state');
    if (emptyEl) emptyEl.style.display = 'none';
    loadSessions();
    if (d.session.report?.pdfPath) {
      toast('这堂课已有报告，点击右上角「报告」可重新生成');
    }
  } catch (e) {
    toast(e.message, 'err');
  }
}

function renderToday(ci) {
  const box = $('#today-list');
  const today = ci?.today || [];
  const dateEl = $('#today-date');
  if (dateEl) dateEl.textContent = fmtDate(new Date());
  if (!today.length) {
    const hint = ci?.hint || '';
    if (box) box.innerHTML = `<div class="side-empty">${esc(hint || '未读取到课表')}</div>`;
    const tb = $('#timetable-body');
    if (tb) tb.innerHTML = ci?.error
      ? `<div class="side-empty">${esc(ci.error)}</div>`
      : `<div class="side-empty">${esc(hint || '今天没有课')}<br><span style="color:var(--ink-3);font-size:12px">课表来源：ClassIsland · ${esc(ci?.profile || '默认档案')}</span></div>`;
    return;
  }
  const now = Date.now();
  if (box) {
    box.innerHTML = '';
    for (const l of today) {
      const start = new Date(l.start).getTime();
      const end = new Date(l.end).getTime();
      const isNow = now >= start && now < end;
      const past = now >= end;
      box.append(el('div', { class: `lesson-row${isNow ? ' now' : past ? ' past' : ''}` }, [
        el('span', { class: 'time', text: fmtTime(l.start) }),
        el('span', { class: 'subj', text: l.subject }),
        isNow ? el('span', { class: 'badge', text: '上课' }) : null,
      ]));
    }
  }
  const table = el('table', { class: 'tl-table' });
  for (const l of today) {
    const start = new Date(l.start).getTime();
    const end = new Date(l.end).getTime();
    const isNow = now >= start && now < end;
    const tr = el('tr', { class: isNow ? 'now' : '' }, [
      el('td', { class: 't', text: `${fmtTime(l.start)} – ${fmtTime(l.end)}` }),
      el('td', { class: 's', text: l.subject }),
      el('td', { class: 'r', text: l.room || (isNow ? '正在上课' : '') }),
    ]);
    table.append(tr);
  }
  const tbody = $('#timetable-body');
  if (tbody) {
    tbody.innerHTML = '';
    tbody.append(table);
  }
  const src = $('#timetable-source');
  if (src) {
    const map = { profile: 'ClassIsland 课表', manual: '手动课表', plugin: 'ClassIsland 注入插件', push: '外部推送' };
    src.textContent = `来源：${map[ci.source] || ci.source}`;
  }
}

/* ---------------- Agent 页 ---------------- */

// Agent 的实现搬到了 agent.js（模型选择 / 步骤轨迹 / 审批），
// 这里只保留与页面切换、状态刷新相关的粘合代码。
function renderAgentPage() {
  renderAgentPageImpl();
}

function askAgent() {
  askAgentImpl();
}

// loadSlots 把启用插件占用的界面槽位渲染出来。
async function loadSlots() {
  try {
    const d = await api.get('/api/plugins/slots');
    renderSlots(d.slots || []);
  } catch (e) { /* 插件槽位是增强项，失败不影响主流程 */ }
}

// refreshShell 样式切换后刷新依赖布局的部件。
function refreshShell() {
  renderComposerBar();
  loadRailSessions();
  loadSlots();
  saveConfig(true);
}

// 供 shell.js 的会话栏调用
S.openSession = (id) => openSession(id);
S.refreshSession = () => refreshLiveSession();

function setView(v) {
  S.view = v;
  $$('.rail-sub-item').forEach((b) => b.classList.toggle('active', b.dataset.view === v));
  $$('#view-switch button').forEach((b) => b.classList.toggle('active', b.dataset.view === v));
  $$('.view').forEach((sec) => { sec.hidden = sec.id !== 'view-' + v || S.page !== 'home'; });
  if (S.page === 'home') $('#page-title').textContent = VIEW_TITLE[v] || '课堂实录';
  if (v === 'history') loadSessions();
  if (v === 'timetable') renderToday(S.status?.classIsland);
}

/* ---------------- 配置保存 ---------------- */

export async function saveConfig(silent = false) {
  try {
    const d = await api.post('/api/config', S.cfg);
    S.cfg = d.config.config;
    if (!silent) toast('设置已保存', 'ok');
    return true;
  } catch (e) {
    toast('保存失败：' + e.message, 'err');
    return false;
  }
}

/* ---------------- 事件 ---------------- */

const handlers = {
  session(data) {
    S.session = data;
    S.live = true;
    $('#timeline').innerHTML = '';
    S.cards.clear();
    renderTimeline(data);
    renderInspector(data);
    loadSessions();
    if (S.page === 'agent') renderAgentPage();
  },
  session_ended(data) {
    S.session = data;
    S.live = false;
    renderTimeline(data);
    renderInspector(data);
    refreshStatus();
    loadSessions();
    if (S.page === 'agent') renderAgentPage();
    toast('课堂已结束，可生成报告', 'ok');
  },
  slide(data) {
    renderSlideEvent(data);
    refreshLiveSession();
    if (data.reason === '翻页') toast(`检测到翻页 · 第 ${data.slideIndex || data.index} 页`);
  },
  transcript(data) {
    $('#transcript').textContent = data.full || data.text || '';
    const card = S.cards.get(data.segmentId);
    if (card) {
      const seg = (S.session?.segments || []).find((x) => x.id === data.segmentId);
      if (seg) { seg.transcript = data.full || data.text; renderCard(card, seg); }
    }
  },
  analysis(data) {
    const seg = data.segment;
    if (!seg) return;
    if (S.session) {
      const i = (S.session.segments || []).findIndex((x) => x.id === seg.id);
      if (i >= 0) S.session.segments[i] = seg;
    }
    upsertCard(seg);
    renderStats(S.session || { segments: [seg], startedAt: seg.createdAt });
  },
  segment_status(data) {
    const seg = (S.session?.segments || []).find((x) => x.id === data.segmentId);
    if (seg) {
      seg.status = data.status;
      if (data.error) seg.error = data.error;
      const card = S.cards.get(data.segmentId);
      if (card) renderCard(card, seg);
    }
  },
  report(data) {
    toast('报告已生成', 'ok');
  },
  agent_step(data) { agentEvent('agent_step', data); },
  agent_tool(data) { agentEvent('agent_tool', data); },
  agent_tool_result(data) { agentEvent('agent_tool_result', data); },
  agent_approval(data) { agentEvent('agent_approval', data); },
  agent_final(data) { agentEvent('agent_final', data); },
  agent_error(data) { agentEvent('agent_error', data); },
  error(data) {
    if (data?.scope === 'audio') toast('麦克风不可用：' + data.message, 'err');
    else toast(data?.message || '发生错误', 'err');
  },
  auto_start(data) { toast(`检测到上课（${data.subject}），已自动开始记录`, 'ok'); },
  auto_stop() { toast('已下课，正在整理课堂记录', 'ok'); },
  ui_command(data) {
    const action = data?.action;
    if (action === 'settings') openSettings(S);
    else if (action === 'toggle') toggleLesson();
  },
  class_state(data) {
    if (data?.inClass && data.subject) {
      $('#composer-hint').textContent = `正在上 ${data.subject}，可以随时提问`;
    }
  },
};

/* ---------------- 全局绑定 ---------------- */

function bindGlobal() {
  $('#btn-toggle-lesson').addEventListener('click', toggleLesson);
  $('#btn-empty-start')?.addEventListener('click', toggleLesson);
  $('#btn-empty-settings')?.addEventListener('click', () => openSettings(S));
  $('#btn-capture').addEventListener('click', captureNow);
  $('#btn-report').addEventListener('click', makeReport);
  $('#btn-ask').addEventListener('click', ask);
  $('#btn-settings').addEventListener('click', () => openSettings(S));
  $('#btn-theme').addEventListener('click', cycleTheme);
  $('#btn-zoom').addEventListener('click', () => { const i = $('#slide-img'); if (i.src) openLightbox(i.src); });

  // 左侧导航
  $('#rail-toggle').addEventListener('click', toggleRail);
  $('#rail-home').addEventListener('click', () => {
    if (S.page === 'home') {
      $('#rail-home').classList.toggle('closed');
      return;
    }
    setPage('home');
  });
  $$('.rail-item[data-page]').forEach((b) => b.addEventListener('click', () => setPage(b.dataset.page)));
  $$('.rail-sub-item').forEach((b) => b.addEventListener('click', () => {
    if (S.page !== 'home') setPage('home');
    setView(b.dataset.view);
  }));
  $$('#view-switch button').forEach((b) => b.addEventListener('click', () => setView(b.dataset.view)));

  // Agent 页的按键绑定在 agent.js 里（避免重复绑定导致一次提问发两遍）

  $('#btn-utterance')?.addEventListener('click', async () => {
    const text = window.prompt('补录一段课堂转写（会归入当前这一页）：');
    if (!text || !text.trim()) return;
    try { await api.post('/api/lesson/utterance', { text: text.trim() }); toast('已补录', 'ok'); }
    catch (e) { toast(e.message, 'err'); }
  });
  $('#lightbox').addEventListener('click', () => { $('#lightbox').hidden = true; });

  const input = $('#ask-input');
  input.addEventListener('input', () => {
    input.style.height = 'auto';
    input.style.height = Math.min(120, input.scrollHeight) + 'px';
  });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ask(); }
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      $('#lightbox').hidden = true;
    }
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault(); saveConfig();
    }
  });

  // 历史条目首次渲染后刷新状态
  setTimeout(refreshStatus, 300);
}

init();
