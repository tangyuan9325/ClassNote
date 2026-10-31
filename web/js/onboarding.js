import { api } from './api.js';
import { $, $$, el, toast, esc } from './util.js';

let cfg = null;
let boot = null;
let onDone = null;
let step = 1;

export function startOnboarding(opts) {
  boot = opts.boot;
  cfg = JSON.parse(JSON.stringify(opts.boot.config.config));
  onDone = opts.onDone;
  $('#onboarding').hidden = false;
  $('#app').hidden = true;

  renderPresets();
  renderWebSites();
  bind();
  goto(1);
}

function goto(n) {
  step = n;
  $$('.ob-panel').forEach((p) => p.classList.toggle('active', Number(p.dataset.panel) === n));
  $$('.ob-step').forEach((s) => {
    const k = Number(s.dataset.step);
    s.classList.toggle('active', k === n);
    s.classList.toggle('done', k < n);
  });
  if (n === 3) { loadEnv(); bindVoice(); }
}

function renderPresets() {
  const grid = $('#preset-grid');
  const list = boot.presets || [];
  grid.innerHTML = '';
  const current = cfg.provider.preset || 'deepseek';
  for (const p of list) {
    const b = el('button', { class: 'preset' + (p.id === current ? ' active' : '') }, [
      el('b', { text: p.name }),
      el('span', { text: p.note || (p.vision ? '支持图片' : '文本模型') }),
    ]);
    b.addEventListener('click', () => {
      cfg.provider.preset = p.id;
      cfg.provider.protocol = p.protocol;
      if (p.baseURL) cfg.provider.baseURL = p.baseURL;
      cfg.provider.model = (p.models && p.models[0]) || '';
      cfg.provider.vision = !!p.vision;
      $$('#preset-grid .preset').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      syncForm();
    });
    grid.append(b);
  }
  syncForm();
}

function renderWebSites() {
  const grid = $('#web-grid');
  const list = boot.webSites || [];
  grid.innerHTML = '';
  for (const s of list) {
    const b = el('button', { class: 'preset' + (s.id === cfg.webAI.site ? ' active' : '') }, [
      el('b', { text: s.name }),
      el('span', { text: s.note || s.url }),
    ]);
    b.addEventListener('click', () => {
      cfg.webAI.site = s.id;
      cfg.webAI.url = s.url;
      cfg.webAI.selectors = s.selectors || {};
      $$('#web-grid .preset').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
    });
    grid.append(b);
  }
}

function syncForm() {
  $('#ob-baseurl').value = cfg.provider.baseURL || '';
  $('#ob-model').value = cfg.provider.model || '';
  $('#ob-apikey').value = cfg.provider.apiKey || '';
  $('#ob-vision').checked = !!cfg.provider.vision;
  const dl = $('#model-options');
  dl.innerHTML = '';
  const preset = (boot.presets || []).find((p) => p.id === cfg.provider.preset);
  for (const m of (preset?.models || [])) {
    const o = document.createElement('option');
    o.value = m;
    dl.append(o);
  }
}

function readForm() {
  cfg.provider.baseURL = $('#ob-baseurl').value.trim();
  cfg.provider.model = $('#ob-model').value.trim();
  cfg.provider.apiKey = $('#ob-apikey').value.trim();
  cfg.provider.vision = $('#ob-vision').checked;
}

function setMode(mode) {
  cfg.provider.mode = mode;
  $$('#ob-mode-seg .seg-item').forEach((b) => b.classList.toggle('active', b.dataset.mode === mode));
  $('#ob-api').hidden = mode !== 'api';
  $('#ob-web').hidden = mode !== 'web';
}

async function saveAll(extra = {}) {
  Object.assign(cfg, extra);
  const d = await api.post('/api/config', cfg);
  cfg = d.config.config;
  return d;
}

function bind() {
  $('#ob-next-1').addEventListener('click', () => {
    $('#onboarding').classList.add('leaving');
    goto(2);
    $('#onboarding').classList.remove('leaving');
  });
  $('#ob-skip').addEventListener('click', async () => {
    try {
      await saveAll({ onboarded: true });
      $('#onboarding').hidden = true;
      onDone();
    } catch (e) { toast(e.message, 'err'); }
  });
  $('#ob-back-1').addEventListener('click', () => goto(1));
  $('#ob-back-2').addEventListener('click', () => goto(2));

  $$('#ob-mode-seg .seg-item').forEach((b) => {
    b.addEventListener('click', () => setMode(b.dataset.mode));
  });

  $('#ob-test').addEventListener('click', async () => {
    readForm();
    const box = $('#ob-test-result');
    box.hidden = false;
    box.className = 'test-result';
    box.textContent = cfg.provider.mode === 'web' ? '正在启动浏览器并测试网页版…' : '正在连接模型…';
    try {
      const d = await api.post('/api/provider/test', { config: cfg });
      if (d.ok) {
        box.className = 'test-result ok';
        box.textContent = `连接成功（${d.latencyMs} ms）\n模型：${d.model}\n回复：${d.reply}`;
      } else {
        box.className = 'test-result err';
        box.textContent = '连接失败：' + (d.error || '未知错误');
      }
    } catch (e) {
      box.className = 'test-result err';
      box.textContent = '连接失败：' + e.message;
    }
  });

  $('#ob-next-2').addEventListener('click', async () => {
    readForm();
    if (cfg.provider.mode === 'api' && (!cfg.provider.baseURL || !cfg.provider.model)) {
      toast('请先填写 Base URL 与模型名', 'err');
      return;
    }
    try {
      await saveAll();
      goto(3);
    } catch (e) { toast(e.message, 'err'); }
  });

  $('#ob-web-open').addEventListener('click', async () => {
    try {
      const d = await api.post('/api/webai/login');
      $('#ob-web-status').textContent = d.message || (d.ok ? '已打开浏览器' : d.error || '打开失败');
    } catch (e) { toast(e.message, 'err'); }
  });
  $('#ob-web-check').addEventListener('click', async () => {
    $('#ob-web-status').textContent = '检测中…';
    try {
      const d = await api.post('/api/webai/check');
      $('#ob-web-status').textContent = d.message;
      if (d.ok) toast('登录状态可用', 'ok');
    } catch (e) { $('#ob-web-status').textContent = e.message; }
  });

  $('#icc-inject').addEventListener('click', async () => {
    try {
      const d = await api.post('/api/integrations/icc/inject', {});
      if (d.ok) { toast(d.message || '已注入', 'ok'); loadEnv(); }
      else toast(d.error || '注入失败', 'err');
    } catch (e) { toast(e.message, 'err'); }
  });
  $('#icc-eject').addEventListener('click', async () => {
    try {
      await api.post('/api/integrations/icc/eject', {});
      toast('已移除插件', 'ok');
      loadEnv();
    } catch (e) { toast(e.message, 'err'); }
  });
  $('#icc-pick').addEventListener('click', async () => {
    try {
      const d = await api.get('/api/dialog/file?title=' + encodeURIComponent('选择 InkCanvasForClass.exe') + '&filter=' + encodeURIComponent('程序 (*.exe)|*.exe'));
      if (d.path) {
        cfg.integrations.icc.exePath = d.path;
        await saveAll();
        toast('已记录 ICC 路径', 'ok');
        loadEnv();
      }
    } catch (e) { toast(e.message, 'err'); }
  });

  $('#ob-finish').addEventListener('click', async () => {
    try {
      await saveAll({ onboarded: true });
      $('#onboarding').hidden = true;
      toast('配置完成，欢迎使用 ClassNote', 'ok');
      onDone();
    } catch (e) { toast(e.message, 'err'); }
  });
}

function bindVoice() {
  const mode = $('#voice-mode');
  const model = $('#voice-model');
  if (!mode) return;
  const asr = cfg.asr || (cfg.asr = {});
  mode.value = asr.mode === 'api' ? 'api' : (asr.mode === 'off' ? 'off' : 'local');
  model.value = asr.localModel || 'ggml-small-q5_1.bin';
  mode.addEventListener('change', () => {
    asr.mode = mode.value;
    $('#voice-model').disabled = mode.value !== 'local';
    $('#voice-tag').textContent = mode.value === 'local' ? '本地模型' : (mode.value === 'api' ? '云端接口' : '未启用');
  });
  model.addEventListener('change', () => {
    asr.localModel = model.value;
  });
  $('#voice-test').addEventListener('click', async () => {
    const box = $('#voice-result');
    box.hidden = false;
    box.className = 'test-result';
    box.textContent = '测试中…';
    try {
      // 用系统合成的一小段语音做不了，这里改成检查后端状态
      const d = await api.get('/api/env');
      const it = (d.items || []).find((x) => x.id === 'asr' || x.id === 'whisper' || /语音|转写|ASR/i.test(x.name || ''));
      if (it && it.ok) {
        box.className = 'test-result ok';
        box.textContent = '语音模型就绪：' + (it.detail || it.name);
      } else {
        box.className = 'test-result warn';
        box.textContent = '语音模型未就绪：' + ((it && (it.hint || it.detail)) || '请检查模型文件');
      }
    } catch (e) {
      box.className = 'test-result err';
      box.textContent = e.message;
    }
  });
  $('#voice-model').disabled = mode.value !== 'local';
}

async function loadEnv() {
  const box = $('#env-list');
  box.innerHTML = '<div class="side-empty">检测中…</div>';
  try {
    const [env, integ] = await Promise.all([api.get('/api/env'), api.get('/api/integrations/status')]);
    box.innerHTML = '';
    for (const it of env.items || []) {
      const dot = it.ok ? 'ok' : (it.optional ? 'warn' : 'err');
      const row = el('div', { class: 'env-row' }, [
        el('span', { class: 'env-dot ' + dot }),
        el('div', { class: 'env-main' }, [
          el('div', { class: 'env-name' }, [
            el('span', { text: it.name }),
            it.optional ? el('span', { class: 'tag', text: '可选' }) : null,
          ]),
          el('div', { class: 'env-detail', text: it.detail || '' }),
          it.hint ? el('div', { class: 'env-hint', text: it.hint }) : null,
        ]),
        el('span', { class: 'tag' + (it.ok ? ' on' : ''), text: it.ok ? '就绪' : '待处理' }),
      ]);
      box.append(row);
    }
    const icc = integ.icc;
    const iccBox = $('#icc-box');
    iccBox.hidden = !(icc.running || icc.installDir);
    $('#icc-tag').textContent = icc.pluginInstalled ? '已注入' : (icc.running ? '运行中 · 未注入' : '未运行');
    $('#icc-tag').className = 'tag' + (icc.pluginInstalled ? ' on' : '');
    $('#icc-hint').textContent = icc.pluginInstalled
      ? '桥接插件已安装。重启 ICC 后生效；未注入时仍可通过系统接口检测放映。'
      : '注入桥接插件后，ICC 的放映与翻页事件会实时推送过来；未注入时仍可通过系统接口检测放映。';
  } catch (e) {
    box.innerHTML = `<div class="side-empty">检测失败：${esc(e.message)}</div>`;
  }
}
