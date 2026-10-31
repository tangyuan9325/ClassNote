import { api } from './api.js';
import { $, $$, el, toast, esc } from './util.js';
import { applyTheme } from './theme.js';

let S = null;
let tab = 'model';

const SUBJECTS = ['语文', '数学', '英语', '物理', '化学', '生物', '政治', '历史', '地理', '信息技术', '体育', '音乐', '美术'];

const WEEK = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];

export function openSettings(state, initialTab) {
  S = state;
  $('#drawer').hidden = false;
  $('#drawer-mask').hidden = false;
  $('#drawer-saved').textContent = '';
  bind();
  if (initialTab) {
    tab = initialTab;
    $$('#drawer-tabs button').forEach((b) => b.classList.toggle('active', b.dataset.tab === tab));
  }
  renderTab(tab);
}

function close() {
  $('#drawer').hidden = true;
  $('#drawer-mask').hidden = true;
  // 关闭时丢弃取色预览，还原真实主题
  if (S?.cfg?.ui?.theme) applyTheme(S.cfg.ui.theme, S.cfg.ui.customThemes);
}

function bind() {
  if (bind.done) return;
  bind.done = true;
  $('#drawer-close').addEventListener('click', close);
  $('#drawer-mask').addEventListener('click', close);
  $$('#drawer-tabs button').forEach((b) => {
    b.addEventListener('click', () => {
      tab = b.dataset.tab;
      $$('#drawer-tabs button').forEach((x) => x.classList.toggle('active', x === b));
      renderTab(tab);
    });
  });
  $('#drawer-save').addEventListener('click', save);
}

async function save() {
  readTab(tab);
  try {
    const d = await api.post('/api/config', S.cfg);
    S.cfg = d.config.config;
    $('#drawer-saved').textContent = '已保存 · ' + new Date().toLocaleTimeString();
    toast('设置已保存', 'ok');
  } catch (e) {
    toast('保存失败：' + e.message, 'err');
  }
}

// 保存配置（不弹提示），与 app.js 的同名函数等价——本模块自持，避免循环依赖。
async function saveConfig(silent = false) {
  try {
    const d = await api.post('/api/config', S.cfg);
    S.cfg = d.config.config;
    return true;
  } catch (e) {
    if (!silent) toast('保存失败：' + e.message, 'err');
    return false;
  }
}

/* ---------------- 渲染 ---------------- */

function renderTab(name) {
  const body = $('#drawer-body');
  const cfg = S.cfg;
  const map = { model: modelTab, asr: asrTab, capture: captureTab, lesson: lessonTab, integrations: integTab, appearance: appearTab, access: accessTab, notify: notifyTab, agent: agentTab, about: aboutTab };
  body.innerHTML = (map[name] || modelTab)(cfg);
  bindTab(name);
}

function modelTab(cfg) {
  const p = cfg.provider;
  const presets = (S.boot.presets || []).map((x) =>
    `<button class="preset ${x.id === p.preset ? 'active' : ''}" data-preset="${esc(x.id)}">
       <b>${esc(x.name)}</b><span>${esc(x.note || '')}</span></button>`).join('');
  const webSites = (S.boot.webSites || []).map((x) =>
    `<button class="preset ${x.id === cfg.webAI.site ? 'active' : ''}" data-site="${esc(x.id)}">
       <b>${esc(x.name)}</b><span>${esc(x.note || x.url)}</span></button>`).join('');
  return `
    <div class="seg" id="s-mode">
      <button class="seg-item ${p.mode === 'api' ? 'active' : ''}" data-mode="api">API 接入</button>
      <button class="seg-item ${p.mode === 'web' ? 'active' : ''}" data-mode="web">网页版 AI</button>
    </div>

    <div id="s-api" ${p.mode === 'web' ? 'hidden' : ''}>
      <div class="desc">支持任何 OpenAI 兼容接口。若模型支持视觉，可直接把幻灯片图片交给它分析。</div>
      <div class="preset-grid">${presets}</div>
      <div class="field-row two">
        <label class="field"><span>Base URL</span><input id="f-base" value="${esc(p.baseURL)}"></label>
        <label class="field"><span>模型</span><input id="f-model" value="${esc(p.model)}"></label>
      </div>
      <label class="field"><span>API Key</span><input id="f-key" type="password" value="${esc(p.apiKey)}" autocomplete="off"></label>
      <div class="field-row two">
        <label class="field"><span>温度</span><input id="f-temp" type="number" step="0.1" min="0" max="1.5" value="${p.temperature}"></label>
        <label class="field"><span>最大输出 tokens</span><input id="f-maxtok" type="number" min="256" max="32000" value="${p.maxTokens}"></label>
      </div>
      <label class="check"><input type="checkbox" id="f-vision" ${p.vision ? 'checked' : ''}> <span>模型支持图片理解</span></label>
      <button class="btn" id="f-test">测试连接</button>
      <div class="test-result" id="f-test-result" hidden></div>
    </div>

    <div id="s-web" ${p.mode === 'api' ? 'hidden' : ''}>
      <div class="desc">用浏览器打开网页版 AI，模拟输入并读取回答。首次需要登录，登录态保存在本机。</div>
      <div class="preset-grid">${webSites}</div>
      <label class="field"><span>页面地址</span><input id="f-weburl" value="${esc(cfg.webAI.url)}"></label>
      <label class="check"><input type="checkbox" id="f-headless" ${cfg.webAI.headless ? 'checked' : ''}> <span>后台运行浏览器（不显示窗口）</span></label>
      <div class="web-login-box">
        <div><b>登录状态</b><p id="f-webstatus">${cfg.webAI.loggedIn ? '上次检测可用' : '尚未检测'}</p></div>
        <div class="web-login-actions">
          <button class="btn small" id="f-webopen">打开登录</button>
          <button class="btn small" id="f-webcheck">检测</button>
        </div>
      </div>
    </div>`;
}

function asrTab(cfg) {
  const a = cfg.asr;
  return `
    <div class="desc">语音转写使用 OpenAI 兼容的 transcriptions 接口（SiliconFlow、OpenAI、Groq 等），也支持本地 whisper.cpp。</div>
    <label class="field"><span>转写方式</span>
      <select id="f-asrmode">
        <option value="api" ${a.mode === 'api' ? 'selected' : ''}>接口转写</option>
        <option value="local" ${a.mode === 'local' ? 'selected' : ''}>本地 whisper.cpp</option>
        <option value="off" ${a.mode === 'off' ? 'selected' : ''}>关闭（只记幻灯片）</option>
      </select>
    </label>
    <div id="f-asr-api" ${a.mode !== 'api' ? 'hidden' : ''}>
      <div class="field-row two">
        <label class="field"><span>Base URL</span><input id="f-asrbase" value="${esc(a.baseURL)}"></label>
        <label class="field"><span>模型</span><input id="f-asrmodel" value="${esc(a.model)}"></label>
      </div>
      <label class="field"><span>API Key</span><input id="f-asrkey" type="password" value="${esc(a.apiKey)}" autocomplete="off"></label>
    </div>
    <div id="f-asr-local" ${a.mode !== 'local' ? 'hidden' : ''}>
      <label class="field"><span>whisper.cpp 可执行文件</span><input id="f-asexe" value="${esc(a.localExe)}" placeholder="C:\\whisper\\main.exe"></label>
      <label class="field"><span>模型文件 (.bin)</span><input id="f-asmodel" value="${esc(a.localModel)}" placeholder="ggml-medium.bin"></label>
    </div>
    <div class="field-row two">
      <label class="field"><span>语言</span><input id="f-aslang" value="${esc(a.language)}" placeholder="zh"></label>
      <label class="field"><span>每批最长（秒）</span><input id="f-aschunk" type="number" min="3" max="30" value="${a.chunkSec}" title="超过长度时把最后一句留给下一批"></label>
    </div>
    <div class="field-row two">
      <label class="field"><span>静音断句（毫秒）</span><input id="f-assil" type="number" min="200" max="3000" step="50" value="${a.silenceMs}"></label>
      <label class="field"><span>语音阈值</span><input id="f-asth" type="number" min="0.002" max="0.2" step="0.002" value="${a.threshold}"></label>
    </div>
    <div class="desc">超过批次长度时，会回溯到最近一次句间停顿，把最后一句留到下一批，再送当前批次转写。阈值越低越灵敏；教室噪声大时可适当提高。录音设备留空使用系统默认。</div>
    <button class="btn" id="f-mictest">检测麦克风（2.5 秒）</button>
    <div class="test-result" id="f-mic-result" hidden></div>`;
}

function captureTab(cfg) {
  const c = cfg.capture;
  return `
    <h3>检测方式</h3>
    <div class="desc">优先通过 PowerPoint / WPS 的 COM 接口读取页码并导出高清图片（画面干净、无批注遮挡）；接口不可用时退回到窗口截图 + 画面差异检测。</div>
    <label class="field"><span>轮询间隔（毫秒）</span><input id="f-capint" type="number" min="200" max="5000" step="100" value="${c.intervalMs}"></label>
    <div class="switch-row"><div><div class="label">翻页自动截图</div><div class="sub">检测到换页时立即抓取</div></div>
      <label class="switch"><input type="checkbox" id="f-capa" ${c.autoOnSlide ? 'checked' : ''}><i></i></label></div>
    <div class="switch-row"><div><div class="label">高清导出（COM）</div><div class="sub">1920×1080，不受悬浮窗与批注影响</div></div>
      <label class="switch"><input type="checkbox" id="f-caph" ${c.exportHighRes ? 'checked' : ''}><i></i></label></div>
    <div class="switch-row"><div><div class="label">裁掉黑边</div><div class="sub">自动去除放映上下/左右的黑边</div></div>
      <label class="switch"><input type="checkbox" id="f-capc" ${c.cropBlackBars ? 'checked' : ''}><i></i></label></div>
    <h3>图像质量</h3>
    <div class="field-row two">
      <label class="field"><span>最大宽度</span><input id="f-capw" type="number" min="640" max="3840" step="80" value="${c.maxWidth}"></label>
      <label class="field"><span>JPEG 质量</span><input id="f-capq" type="number" min="50" max="100" value="${c.jpegQuality}"></label>
    </div>
    <label class="field"><span>画面差异阈值（0-64，越大越迟钝）</span><input id="f-capd" type="number" min="0" max="40" value="${c.diffThreshold}"></label>
    <div class="desc">手动截图的快捷键在程序启动时注册为 Ctrl+Alt+S（可在 config.json 修改）。</div>`;
}

function lessonTab(cfg) {
  const l = cfg.lesson;
  const chips = SUBJECTS.map((s) =>
    `<button class="chip-toggle ${l.subjects.includes(s) ? 'on' : ''}" data-subject="${esc(s)}">${esc(s)}</button>`).join('');
  const tt = (l.manualTimetable || [])
    .map((e) => `${WEEK[e.weekday]} ${e.start}-${e.end} ${e.subject}`).join('\n');
  return `
    <h3>监听的学科</h3>
    <div class="desc">只有在这些学科的课表时段，才会自动开始记录。默认只监听语文课。</div>
    <div class="subject-chips" id="f-subjects">${chips}</div>

    <h3>行为</h3>
    <div class="switch-row"><div><div class="label">上课自动开始</div><div class="sub">由 ClassIsland 课表触发</div></div>
      <label class="switch"><input type="checkbox" id="f-autostart" ${l.autoStart ? 'checked' : ''}><i></i></label></div>
    <div class="switch-row"><div><div class="label">下课自动结束</div><div class="sub">只结束自动开始的课堂</div></div>
      <label class="switch"><input type="checkbox" id="f-autostop" ${l.autoStop ? 'checked' : ''}><i></i></label></div>
    <div class="switch-row"><div><div class="label">下课后自动生成报告</div><div class="sub">并导出 PDF</div></div>
      <label class="switch"><input type="checkbox" id="f-autoreport" ${l.autoReport ? 'checked' : ''}><i></i></label></div>
    <div class="switch-row"><div><div class="label">逐页 AI 整理</div><div class="sub">翻页后调用模型生成笔记</div></div>
      <label class="switch"><input type="checkbox" id="f-analyze" ${l.analyzeSlides ? 'checked' : ''}><i></i></label></div>

    <h3>备用课表</h3>
    <div class="desc">ClassIsland 未运行时，可用下面的手动课表。每行格式：周几 HH:MM-HH:MM 科目</div>
    <label class="field"><textarea id="f-tt" rows="4" style="padding:8px 11px;border-radius:9px;border:1px solid var(--line-strong);background:var(--bg-panel);outline:none;font-size:12.5px;line-height:1.7">${esc(tt)}</textarea></label>`;
}

function integTab(cfg) {
  const ci = cfg.integrations.classIsland;
  const icc = cfg.integrations.icc;
  return `
    <h3>ClassIsland 课表</h3>
    <div class="desc">读取 ClassIsland 的课表文件判断上下课与学科。可选择程序本体，程序会自动定位 data 目录。</div>
    <label class="field"><span>ClassIsland.exe</span><input id="f-ciexe" value="${esc(ci.exePath)}" placeholder="留空则自动探测"></label>
    <label class="field"><span>数据目录</span><input id="f-cidir" value="${esc(ci.dataDir)}" placeholder="…\\ClassIsland\\data"></label>
    <div class="switch-row"><div><div class="label">启用课表联动</div><div class="sub">关闭后只能手动开始/结束</div></div>
      <label class="switch"><input type="checkbox" id="f-cien" ${ci.enabled ? 'checked' : ''}><i></i></label></div>
    <div class="panel" style="margin:10px 0 18px;padding:12px 14px">
      <div class="kv"><span>当前状态</span><b id="f-cistate">检测中…</b></div>
      <div class="kv"><span>课表文件</span><b id="f-cifile">—</b></div>
    </div>
    <div style="display:flex;gap:6px;margin-bottom:6px">
      <button class="btn small" id="f-cipick">选择 ClassIsland.exe</button>
      <button class="btn small ghost" id="f-cireload">刷新状态</button>
    </div>

    <h3>ClassIsland 注入插件</h3>
    <div class="desc">把联动插件装进 ClassIsland 后，上课/下课与当前科目会实时注入，比读取课表文件更准；ClassNote 也能让 ClassIsland 弹出课堂提醒。</div>
    <div class="panel" style="margin:10px 0 12px;padding:12px 14px">
      <div class="kv"><span>安装状态</span><b id="f-ciplugin">检测中…</b></div>
      <div class="kv"><span>连接</span><b id="f-cipluginlink">—</b></div>
      <div class="kv"><span>插件目录</span><b id="f-ciplugindir" style="font-size:11px;font-weight:400">—</b></div>
    </div>
    <div style="display:flex;gap:6px;flex-wrap:wrap">
      <button class="btn small primary" id="f-ciinstall">安装 / 更新插件</button>
      <button class="btn small" id="f-citest">测试连接</button>
      <button class="btn small" id="f-cinotify">发送测试提醒</button>
      <button class="btn small danger" id="f-ciuninstall">卸载插件</button>
    </div>
    <div class="desc" style="margin-top:10px">安装后需要重启 ClassIsland 才会加载插件；状态接口只监听本机 127.0.0.1。</div>

    <h3>InkCanvasForClass CE</h3>
    <div class="desc">ICC 运行时注入桥接插件，可实时接收放映/翻页事件；未注入也能正常检测放映。</div>
    <label class="field"><span>InkCanvasForClass.exe</span><input id="f-iccexe" value="${esc(icc.exePath)}" placeholder="留空则自动从进程定位"></label>
    <label class="field"><span>插件目录</span><input id="f-iccdir" value="${esc(icc.pluginDir)}" placeholder="…\\InkCanvasForClass\\Plugins"></label>
    <div class="switch-row"><div><div class="label">启用 ICC 联动</div><div class="sub">检测进程与插件状态</div></div>
      <label class="switch"><input type="checkbox" id="f-iccen" ${icc.enabled ? 'checked' : ''}><i></i></label></div>
    <div class="panel" style="margin:10px 0 14px;padding:12px 14px">
      <div class="kv"><span>运行状态</span><b id="f-iccstate">检测中…</b></div>
      <div class="kv"><span>插件</span><b id="f-iccplugin">—</b></div>
    </div>
    <div style="display:flex;gap:6px;flex-wrap:wrap">
      <button class="btn small" id="f-iccinject">注入插件</button>
      <button class="btn small" id="f-icceject">移除插件</button>
      <button class="btn small ghost" id="f-iccpick">选择 ICC.exe</button>
    </div>
    <div class="desc" style="margin-top:12px">插件随包提供（plugins/ClassNote.Bridge.dll）。ICC 未运行时启动 ClassNote 会自动注入并启用；ICC 正在运行请先退出，否则配置文件会被它覆盖。</div>`;
}

function appearTab(cfg) {
  const themes = S.boot.themes || [];
  const custom = (cfg.ui && cfg.ui.customThemes) || [];
  const cards = themes.map((t) => `
    <button class="theme-card ${cfg.ui.theme === t.id ? 'active' : ''}" data-theme="${esc(t.id)}">
      <div class="theme-swatch"><i style="background:${t.id === 'claude' ? '#faf9f5' : t.id === 'graphite' ? '#1a1a19' : t.id === 'mist' ? '#f4f6f8' : '#141815'}"></i>
      <i style="background:${esc(t.swatch)}"></i><i style="background:${t.id === 'claude' ? '#e6e1d5' : t.id === 'graphite' ? '#312e2a' : t.id === 'mist' ? '#dde4ea' : '#2a332c'}"></i></div>
      <b>${esc(t.name)}</b><span>${esc(t.note)}</span>
    </button>`).join('');
  const customCards = custom.map((t) => `
    <button class="theme-card ${cfg.ui.theme === t.id ? 'active' : ''}" data-theme="${esc(t.id)}">
      <div class="theme-swatch"><i style="background:${esc((t.vars || {}).bg || '#fff')}"></i>
      <i style="background:${esc((t.vars || {}).accent || '#888')}"></i><i style="background:${esc((t.vars || {}).line || '#ddd')}"></i></div>
      <b>${esc(t.name)}</b><span>自定义</span>
    </button>`).join('');
  // 取色器：编辑中主题的当前值（优先载入正编辑的自定义主题，否则给一组协调的默认色）
  const edit = S.editTheme || (S.editTheme = {
    id: 'my-theme', name: '自定义主题',
    vars: { bg: '#faf9f5', 'bg-panel': '#ffffff', ink: '#29261f', 'ink-2': '#6f675c', line: '#e3ddd1', accent: '#c96442' },
  });
  const C = (k, label) => `
    <div class="color-cell">
      <span>${label}</span>
      <input type="color" data-ck="${k}" value="${esc(edit.vars[k] || '#ffffff')}">
      <input class="color-hex" data-ck="${k}" value="${esc(edit.vars[k] || '#ffffff')}" spellcheck="false">
    </div>`;
  return `
    <h3>主题</h3>
    <div class="desc">界面同时适用于本地窗口与浏览器访问，切换后立即生效。</div>
    <div class="theme-grid">${cards}${customCards}</div>

    <h3>自定义主题</h3>
    <div class="theme-editor">
      <div class="row" style="gap:8px;flex-wrap:wrap">
        <label class="field" style="flex:1;min-width:150px;margin-bottom:0"><span>主题名称</span>
          <input id="ct-name" value="${esc(edit.name)}" placeholder="例如：深夜备课"></label>
        <label class="field" style="flex:1;min-width:150px;margin-bottom:0"><span>主题 ID</span>
          <input id="ct-id" value="${esc(edit.id)}" placeholder="my-theme"></label>
      </div>
      <div class="color-grid">
        ${C('bg', '背景')}${C('bg-panel', '面板')}${C('ink', '正文')}${C('ink-2', '次要')}${C('line', '边线')}${C('accent', '强调')}
      </div>
      <div class="theme-preview" id="ct-preview">
        <i style="background:${esc(edit.vars.bg)}"></i><i style="background:${esc(edit.vars['bg-panel'])}"></i>
        <i style="background:${esc(edit.vars.ink)}"></i><i style="background:${esc(edit.vars.accent)}"></i>
      </div>
      <div class="row" style="gap:8px;margin-top:10px">
        <button class="btn small primary" id="ct-save">保存并应用</button>
        <button class="btn small" id="ct-export">导出 JSON</button>
        <button class="btn small" id="ct-import">导入 JSON</button>
        <input type="file" id="ct-file" accept="application/json" hidden>
        <span class="hint" id="ct-result"></span>
      </div>
      <div class="desc" style="margin-top:8px">改动颜色会立即预览；保存后可在上方主题列表选用。社区主题在左侧「主题商店」页。</div>
    </div>

    <h3>显示</h3>
    <label class="field"><span>信息密度</span>
      <select id="f-density">
        <option value="comfortable" ${cfg.ui.density === 'comfortable' ? 'selected' : ''}>舒展</option>
        <option value="compact" ${cfg.ui.density === 'compact' ? 'selected' : ''}>紧凑</option>
      </select>
    </label>
    <div class="desc">浏览器访问地址：<b>${esc(S.boot.app.url)}</b><br>本地窗口与网页端共用同一套界面与数据。</div>`;
}

function accessTab(cfg) {
  const srv = S.boot.server || {};
  const sc = cfg.server || {};
  const urls = (srv.urls || []).map((u) => `
    <div class="row" style="gap:6px;align-items:center">
      <code style="flex:1;font-size:11px;background:var(--bg-2);padding:6px 8px;border-radius:6px;overflow:auto;white-space:nowrap">${esc(u)}</code>
      <button class="btn small" data-copy="${esc(u)}">复制</button>
    </div>`).join('');
  return `
    <h3>访问控制</h3>
    <div class="field">
      <label>访问范围</label>
      <div class="seg" id="f-access">
        <button data-v="local" class="${sc.access !== 'lan' ? 'active' : ''}">仅本机</button>
        <button data-v="lan" class="${sc.access === 'lan' ? 'active' : ''}">允许局域网</button>
      </div>
      <div class="desc">开启局域网后，同一 WiFi 下的手机/平板可以用下方链接访问。所有外部访问都需要密钥。</div>
    </div>
    <div class="field">
      <label>访问密钥</label>
      <div class="row" style="gap:6px;align-items:center">
        <input id="f-token" value="${esc(sc.token || '')}" readonly style="flex:1;font-family:monospace">
        <button class="btn small" id="btn-copy-token">复制</button>
        <button class="btn small" id="btn-new-token">重新生成</button>
      </div>
      <div class="desc">密钥在首次启动时随机生成，修改后永久保存到配置。链接里带 <code>?token=密钥</code> 即可免密进入。</div>
    </div>
    <h3>访问链接</h3>
    <div class="stack" style="gap:6px">
      ${urls || '<div class="desc">暂无可用链接</div>'}
    </div>
    <div class="desc" style="margin-top:10px">本机访问 <code>${esc((srv.urls || [''])[0])}</code> 无需密钥。</div>
  `;
}

function bindAccess() {
  $$('#f-access button').forEach((b) => b.addEventListener('click', () => {
    $$('#f-access button').forEach((x) => x.classList.remove('active'));
    b.classList.add('active');
    S.cfg.server = S.cfg.server || {};
    S.cfg.server.access = b.dataset.v;
    saveConfig();
    toast(b.dataset.v === 'lan' ? '已允许局域网访问，重启后生效' : '已限制为仅本机', 'ok');
  }));
  $('#btn-copy-token')?.addEventListener('click', () => {
    copy($('#f-token')?.value || '');
  });
  $('#btn-new-token')?.addEventListener('click', () => {
    if (!confirm('重新生成密钥后，旧链接会立刻失效。确定吗？')) return;
    S.cfg.server = S.cfg.server || {};
    S.cfg.server.token = 'cn_' + Array.from(crypto.getRandomValues(new Uint8Array(12))).map((x) => x.toString(16).padStart(2, '0')).join('');
    $('#f-token').value = S.cfg.server.token;
    saveConfig();
    toast('已生成新密钥', 'ok');
  });
  $$('[data-copy]').forEach((b) => b.addEventListener('click', () => copy(b.dataset.copy)));
}

function copy(text) {
  if (!text) return;
  navigator.clipboard?.writeText(text).then(() => toast('已复制', 'ok'), () => toast('复制失败', 'err'));
}

const NOTIFY_TYPES = {
  wecom: '企业微信', dingtalk: '钉钉', feishu: '飞书', telegram: 'Telegram',
  serverchan: 'Server酱', discord: 'Discord', slack: 'Slack', bark: 'Bark',
  pushplus: 'PushPlus', ntfy: 'ntfy', gotify: 'Gotify', webhook: '自定义',
};

function notifyTab(cfg) {
  const nc = cfg.notify || (cfg.notify = { enabled: false, providers: [] });
  const presets = (S.boot.notifyPresets || []);
  const rows = (nc.providers || []).map((p, i) => {
    const t = p.type || 'webhook';
    const needWebhook = ['wecom', 'dingtalk', 'feishu', 'discord', 'slack', 'ntfy', 'gotify', 'webhook'].includes(t);
    const needSecret = ['wecom', 'dingtalk'].includes(t);
    const needToken = ['telegram', 'serverchan', 'bark', 'pushplus', 'gotify'].includes(t);
    const needChat = t === 'telegram';
    return `
    <div class="card np-card" data-i="${i}">
      <div class="np-head">
        <label class="switch"><input type="checkbox" class="np-enabled" data-i="${i}" ${p.enabled ? 'checked' : ''}><i></i></label>
        <input class="np-name" data-i="${i}" value="${esc(p.name || '')}" placeholder="渠道名称" style="flex:1">
        <select class="np-type" data-i="${i}">
          ${Object.entries(NOTIFY_TYPES).map(([k, v]) => `<option value="${k}" ${t === k ? 'selected' : ''}>${v}</option>`).join('')}
        </select>
        <button class="btn small danger np-del" data-i="${i}">删除</button>
      </div>
      <div class="np-fields">
        ${needWebhook ? `<label class="np-field"><span>${t === 'ntfy' ? '主题地址' : t === 'gotify' ? '服务器地址' : 'Webhook'}</span>
          <input class="np-webhook" data-i="${i}" value="${esc(p.webhook || '')}" placeholder="${t === 'ntfy' ? 'https://ntfy.sh/my-topic' : 'https://…'}"></label>` : ''}
        ${needSecret ? `<label class="np-field"><span>加签密钥（可选）</span>
          <input class="np-secret" data-i="${i}" value="${esc(p.secret || '')}" placeholder="加签模式的密钥"></label>` : ''}
        ${needToken ? `<label class="np-field"><span>${t === 'bark' ? 'DeviceKey' : t === 'pushplus' ? 'Token' : t === 'gotify' ? 'App Token' : 'Token / SendKey'}</span>
          <input class="np-token" data-i="${i}" value="${esc(p.token || '')}" placeholder="…"></label>` : ''}
        ${needChat ? `<label class="np-field"><span>ChatID</span>
          <input class="np-chat" data-i="${i}" value="${esc(p.chatId || '')}" placeholder="Telegram ChatID"></label>` : ''}
      </div>
    </div>`;
  }).join('');
  return `
    <h3>机器人通知</h3>
    <div class="np-master">
      <label class="switch"><input type="checkbox" id="np-master" ${nc.enabled ? 'checked' : ''}><i></i></label>
      <div>
        <div class="label">启用通知</div>
        <div class="sub">课堂开始、下课、报告生成时推送到群里</div>
      </div>
    </div>

    <h3>快速添加</h3>
    <input id="np-search" placeholder="搜索渠道，如：微信 / 推送 / iOS…" style="width:100%;margin-bottom:8px">
    <div class="np-presets" id="np-presets">
      ${presets.map((p) => `
        <button class="np-preset" data-preset="${esc(p.id)}" title="${esc(p.hint || '')}">
          <b>${esc(p.name)}</b><span>${esc(p.hint || '')}</span>
        </button>`).join('')}
    </div>

    <h3>已添加渠道</h3>
    <div id="np-list">${rows || '<div class="desc">还没有通知渠道，点上方按钮添加。</div>'}</div>

    <div class="row" style="gap:8px;margin-top:12px">
      <button class="btn" id="np-test">发送测试消息</button>
      <span class="hint" id="np-test-result"></span>
    </div>`;
}

function bindNotify() {
  const nc = S.cfg.notify || (S.cfg.notify = { enabled: false, providers: [] });
  nc.providers = nc.providers || [];
  $('#np-master')?.addEventListener('change', (e) => {
    nc.enabled = e.target.checked;
    saveConfig();
  });
  // 快速添加：搜索过滤
  $('#np-search')?.addEventListener('input', (e) => {
    const q = e.target.value.trim().toLowerCase();
    $$('#np-presets .np-preset').forEach((b) => {
      const hit = !q || b.textContent.toLowerCase().includes(q);
      b.style.display = hit ? '' : 'none';
    });
  });
  $$('#np-presets .np-preset').forEach((b) => b.addEventListener('click', () => {
    const id = b.dataset.preset;
    const meta = (S.boot.notifyPresets || []).find((x) => x.id === id) || {};
    nc.providers.push({ name: meta.name || id, type: id, enabled: true });
    renderTab('notify');
    toast('已添加 ' + (meta.name || id) + '，填好凭据后保存', 'ok');
  }));
  const bind = (sel, key, ev) => $$(sel).forEach((el) => el.addEventListener(ev || 'change', () => {
    const i = +el.dataset.i;
    if (nc.providers[i]) { nc.providers[i][key] = el.type === 'checkbox' ? el.checked : el.value; saveConfig(); }
  }));
  bind('.np-name', 'name', 'change');
  bind('.np-type', 'type', 'change');
  bind('.np-enabled', 'enabled', 'change');
  bind('.np-webhook', 'webhook', 'change');
  bind('.np-secret', 'secret', 'change');
  bind('.np-token', 'token', 'change');
  bind('.np-chat', 'chatId', 'change');
  // 切换类型时重排字段区
  $$('.np-type').forEach((sel) => sel.addEventListener('change', () => {
    const i = +sel.dataset.i;
    if (nc.providers[i]) nc.providers[i].type = sel.value;
    renderTab('notify');
  }));
  $$('.np-del').forEach((b) => b.addEventListener('click', () => {
    nc.providers.splice(+b.dataset.i, 1);
    renderTab('notify');
    saveConfig();
  }));
  $('#np-test')?.addEventListener('click', async () => {
    const box = $('#np-test-result');
    box.textContent = '发送中…';
    try {
      const d = await api.post('/api/notify/test');
      box.textContent = Object.entries(d.results || {}).map(([k, v]) => k + ': ' + v).join(' | ');
    } catch (e) {
      box.textContent = e.message;
    }
  });
}

function bindAppearance() {
  const cfg = S.cfg;
  const custom = (cfg.ui.customThemes) || [];
  $$('.theme-card').forEach((b) => b.addEventListener('click', async () => {
    cfg.ui.theme = b.dataset.theme;
    $$('.theme-card').forEach((x) => x.classList.remove('active'));
    b.classList.add('active');
    applyTheme(cfg.ui.theme, cfg.ui.customThemes);
    saveConfig();
    toast('已切换到 ' + (b.querySelector('b')?.textContent || cfg.ui.theme), 'ok');
  }));
  $('#f-density')?.addEventListener('change', (e) => {
    cfg.ui.density = e.target.value;
    document.documentElement.dataset.density = e.target.value;
    saveConfig();
  });
  const result = (msg, cls) => {
    const r = $('#ct-result');
    if (r) { r.textContent = msg; r.className = 'hint ' + (cls || ''); }
  };
  // 色块 / hex 双向绑定 + 实时预览
  const edit = S.editTheme;
  const syncColor = (k, v, from) => {
    if (!/^#[0-9a-fA-F]{6}$/.test(v)) return;
    edit.vars[k] = v.toLowerCase();
    $$(`[data-ck="${k}"]`).forEach((n) => {
      if (n !== from) n.value = edit.vars[k];
    });
    const pv = $('#ct-preview');
    if (pv && pv.children.length >= 4) {
      pv.children[0].style.background = edit.vars.bg;
      pv.children[1].style.background = edit.vars['bg-panel'];
      pv.children[2].style.background = edit.vars.ink;
      pv.children[3].style.background = edit.vars.accent;
    }
    // 实时预览到整页
    applyTheme('__preview__', [{ id: '__preview__', name: 'preview', vars: edit.vars }]);
  };
  $$('.color-cell input[type=color]').forEach((n) => n.addEventListener('input', () => syncColor(n.dataset.ck, n.value, n)));
  $$('.color-cell .color-hex').forEach((n) => n.addEventListener('change', () => syncColor(n.dataset.ck, n.value.trim(), n)));
  $('#ct-name')?.addEventListener('change', (e) => { edit.name = e.target.value.trim() || '自定义主题'; });
  $('#ct-id')?.addEventListener('change', (e) => { edit.id = e.target.value.trim() || 'my-theme'; });
  $('#ct-save')?.addEventListener('click', async () => {
    const id = ($('#ct-id')?.value || '').trim() || 'my-theme';
    const name = ($('#ct-name')?.value || '').trim() || '自定义主题';
    const vars = { ...edit.vars };
    try {
      await api.post('/api/themes/save', { id, name, vars });
      cfg.ui.customThemes = (cfg.ui.customThemes || []).filter((x) => x.id !== id);
      cfg.ui.customThemes.push({ id, name, vars });
      cfg.ui.theme = id;
      applyTheme(id, cfg.ui.customThemes);
      edit.id = id;
      edit.name = name;
      result('已保存并应用', 'ok');
      renderTab('appearance');
    } catch (e) { result(e.message, 'err'); }
  });
  $('#ct-export')?.addEventListener('click', () => {
    const id = ($('#ct-id')?.value || '').trim() || 'my-theme';
    const name = ($('#ct-name')?.value || '').trim() || '自定义主题';
    const t = custom.find((x) => x.id === id) || { id, name, vars: edit.vars };
    const blob = new Blob([JSON.stringify({ format: 'classnote-theme', version: 1, theme: t }, null, 2)], { type: 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'classnote-theme-' + id + '.json';
    a.click();
    result('已导出', 'ok');
  });
  $('#ct-import')?.addEventListener('click', () => $('#ct-file')?.click());
  $('#ct-file')?.addEventListener('change', async (e) => {
    const f = e.target.files[0];
    if (!f) return;
    try {
      const text = await f.text();
      const d = await api.post('/api/themes/import', JSON.parse(text));
      cfg.ui.customThemes = d.custom || [];
      if (d.theme && d.theme.vars) {
        Object.assign(edit.vars, d.theme.vars);
        edit.id = d.theme.id || edit.id;
        edit.name = d.theme.name || edit.name;
      }
      renderTab('appearance');
      result('已导入 ' + (d.theme && d.theme.name), 'ok');
    } catch (err) { result(err.message, 'err'); }
    e.target.value = '';
  });
}

function agentTab(cfg) {
  cfg.agent = cfg.agent || { permission: 'remind' };
  const levels = [
    ['all', '全部访问', 'Agent 可直接使用全部本地能力，不再询问'],
    ['remind', '敏感操作提醒', '涉及写文件 / 执行命令时先提示（推荐）'],
    ['review', '全部人工审查', '每一步操作都要人工确认'],
  ];
  return `
    <h3>Agent 权限</h3>
    <div class="desc">控制课堂助手调用本地能力（截图、笔记、报告）时的审批强度。</div>
    <div class="seg" id="ag-perm" style="margin:10px 0">
      ${levels.map(([id, name]) => `<button data-v="${id}" class="${cfg.agent.permission === id ? 'active' : ''}">${name}</button>`).join('')}
    </div>
    <div class="desc" id="ag-perm-note">${esc((levels.find((x) => x[0] === cfg.agent.permission) || levels[1])[2])}</div>

    <h3>技能包</h3>
    <div class="desc">技能包是一段可复用的提示词，启用后会并入课堂助手的指令。放在 <code>data/plugins/skills/</code> 里的 JSON 文件。</div>
    <div id="ag-skills"><div class="desc">加载中…</div></div>

    <h3>模型</h3>
    <label class="field"><span>课堂助手使用的模型</span>
      <input id="ag-model" value="${esc(cfg.agent.model || '')}" placeholder="留空则跟随「模型」页的设置">
    </label>
  `;
}

function bindAgent() {
  const cfg = S.cfg;
  cfg.agent = cfg.agent || { permission: 'remind' };
  $$('#ag-perm button').forEach((b) => b.addEventListener('click', async () => {
    $$('#ag-perm button').forEach((x) => x.classList.remove('active'));
    b.classList.add('active');
    cfg.agent.permission = b.dataset.v;
    try {
      await api.post('/api/agent/permission', { permission: b.dataset.v });
      toast('权限已更新', 'ok');
    } catch (e) { toast(e.message, 'err'); }
  }));
  $('#ag-model')?.addEventListener('change', async (e) => {
    cfg.agent.model = e.target.value.trim();
    try {
      await api.post('/api/agent/permission', { model: cfg.agent.model });
    } catch (err) { toast(err.message, 'err'); }
  });
  loadSkills();
}

async function loadSkills() {
  const box = $('#ag-skills');
  if (!box) return;
  try {
    const d = await api.get('/api/plugins');
    box.innerHTML = '';
    for (const sk of d.skills || []) {
      const row = document.createElement('div');
      row.className = 'card';
      row.style.marginBottom = '8px';
      row.innerHTML = `
        <div class="row" style="gap:8px;align-items:center">
          <label class="switch"><input type="checkbox" class="sk-on" data-id="${esc(sk.id)}" ${sk.enabled ? 'checked' : ''}><span></span></label>
          <b style="flex:1">${esc(sk.name)}</b>
          ${sk.builtIn ? '<span class="tag">内置</span>' : ''}
          <button class="btn small danger sk-del" data-id="${esc(sk.id)}">删除</button>
        </div>
        <div class="desc" style="margin-top:6px">${esc(sk.description || '')}</div>
        <div class="desc" style="margin-top:4px;opacity:.75">${esc((sk.prompt || '').slice(0, 120))}</div>
        <div class="desc" style="margin-top:4px">可用工具：${esc((sk.tools || []).join('、') || '无')}</div>`;
      box.append(row);
    }
    if (!(d.skills || []).length) box.innerHTML = '<div class="desc">还没有技能包</div>';
    $$('.sk-on').forEach((el) => el.addEventListener('change', async () => {
      await api.post('/api/plugins/toggle', { id: el.dataset.id, enabled: el.checked });
      toast(el.checked ? '已启用' : '已停用', 'ok');
    }));
    $$('.sk-del').forEach((b) => b.addEventListener('click', async () => {
      if (!confirm('确定删除技能包 ' + b.dataset.id + ' 吗？')) return;
      await api.post('/api/plugins/delete', { id: b.dataset.id });
      loadSkills();
    }));
  } catch (e) {
    box.innerHTML = '<div class="desc">' + esc(e.message) + '</div>';
  }
}

function aboutTab(cfg) {
  cfg.github = cfg.github || {};
  const a = S.boot.app;
  return `
    <h3>ClassNote</h3>
    <div class="kv"><span>版本</span><b>${esc(a.version)} (${esc(a.build)})</b></div>
    <div class="kv"><span>平台</span><b>${esc(a.platform)}</b></div>
    <div class="kv"><span>本地地址</span><b>${esc(a.url)}</b></div>
    <div class="kv"><span>工作目录</span><b>${esc(a.paths.root)}</b></div>
    <div class="kv"><span>课堂记录</span><b>${esc(a.paths.sessions)}</b></div>
    <div class="kv"><span>报告输出</span><b>${esc(a.paths.reports)}</b></div>
    <h3>更新</h3>
    <div class="field">
      <label>GitHub 仓库</label>
      <input id="f-repo" value="${esc((cfg.github && cfg.github.repo) || '')}" placeholder="owner/name">
      <div class="desc">用于检查新版本。仓库需包含 GitHub Release。</div>
    </div>
    <div class="field">
      <label>GitHub Token（可选，私有仓库需要）</label>
      <input id="f-ghtoken" type="password" value="${esc((cfg.github && cfg.github.token) || '')}" placeholder="ghp_...">
    </div>
    <div class="row" style="gap:8px;align-items:center;flex-wrap:wrap">
      <button class="btn small" id="btn-update-check">检查更新</button>
      <label class="switch"><input type="checkbox" id="f-checkupdates" ${cfg.github && cfg.github.checkUpdates ? 'checked' : ''}><i></i></label>
      <span class="hint">启动时自动检查</span>
    </div>
    <div class="test-result" id="update-result" hidden></div>
    <div class="row" style="gap:8px;align-items:center;flex-wrap:wrap;margin-top:8px" id="update-actions" hidden>
      <button class="btn small primary" id="btn-update-download">下载更新包</button>
      <button class="btn small primary" id="btn-update-apply" hidden>安装并重启</button>
      <button class="btn small" id="btn-update-page">打开发布页</button>
      <span class="hint" id="update-progress"></span>
    </div>
    <h3>维护</h3>
    <div style="display:flex;gap:6px;flex-wrap:wrap">
      <button class="btn small" data-open="data">打开数据目录</button>
      <button class="btn small" data-open="reports">打开报告目录</button>
      <button class="btn small" data-open="logs">查看日志</button>
      <button class="btn small" id="f-reset">重置配置</button>
      <button class="btn small" id="f-quit">退出程序</button>
    </div>
    <div class="desc" style="margin-top:14px">本程序以 MIT 许可证开源，第三方组件许可见 README。课堂录音与截图仅保存在本机。</div>`;
}

/* ---------------- 绑定与回读 ---------------- */

function bindTab(name) {
  const cfg = S.cfg;
  if (name === 'access') { bindAccess(); return; }
  if (name === 'notify') { bindNotify(); return; }
  if (name === 'appearance') { bindAppearance(); return; }
  if (name === 'agent') { bindAgent(); return; }
  if (name === 'about') {
    $('#f-repo')?.addEventListener('change', (e) => { cfg.github.repo = e.target.value.trim(); saveConfig(); });
    $('#f-ghtoken')?.addEventListener('change', (e) => { cfg.github.token = e.target.value.trim(); saveConfig(); });
    $('#f-checkupdates')?.addEventListener('change', (e) => { cfg.github.checkUpdates = e.target.checked; saveConfig(); });
    let updateInfo = null;
    let downloadedPath = '';
    $('#btn-update-check')?.addEventListener('click', async () => {
      const box = $('#update-result');
      box.hidden = false;
      box.className = 'test-result';
      box.textContent = '检查中…';
      $('#update-actions').hidden = true;
      try {
        const d = await api.get('/api/update/check');
        updateInfo = d;
        if (d.error) {
          box.className = 'test-result err';
          box.textContent = d.error;
        } else if (d.hasUpdate) {
          box.className = 'test-result ok';
          box.textContent = `发现新版本 ${d.latest}（当前 ${d.current}）${d.notes ? '\n' + d.notes.slice(0, 300) : ''}`;
          $('#update-actions').hidden = false;
          $('#btn-update-apply').hidden = true;
          $('#update-progress').textContent = '';
          if (d.url) $('#btn-update-page').onclick = () => window.open(d.url, '_blank');
        } else {
          box.className = 'test-result ok';
          box.textContent = `已是最新（${d.current}）`;
        }
      } catch (e) { box.className = 'test-result err'; box.textContent = e.message; }
    });
    $('#btn-update-download')?.addEventListener('click', async () => {
      const prog = $('#update-progress');
      prog.textContent = '下载中…';
      try {
        const asset = (updateInfo?.assets || []).find((a) => /\.zip$/i.test(a.name)) || (updateInfo?.assets || [])[0];
        if (!asset) throw new Error('Release 里没有可下载的附件');
        const d = await api.post('/api/update/download', { url: asset.url, name: asset.name });
        downloadedPath = d.path;
        prog.textContent = '已下载：' + asset.name;
        $('#btn-update-apply').hidden = false;
      } catch (e) { prog.textContent = '下载失败：' + e.message; }
    });
    $('#btn-update-apply')?.addEventListener('click', async () => {
      if (!downloadedPath) return;
      if (!confirm('将退出程序并安装更新，完成后自动重启。继续吗？')) return;
      const prog = $('#update-progress');
      prog.textContent = '正在准备升级…';
      try {
        await api.post('/api/update/apply', { path: downloadedPath });
        document.body.innerHTML = '<div style="padding:60px;font-family:sans-serif;color:#666">正在升级 ClassNote…完成后会自动重新打开。</div>';
      } catch (e) { prog.textContent = '升级失败：' + e.message; }
    });
  }
  if (name === 'model') {
    $$('#s-mode .seg-item').forEach((b) => b.addEventListener('click', () => {
      cfg.provider.mode = b.dataset.mode;
      $$('#s-mode .seg-item').forEach((x) => x.classList.toggle('active', x === b));
      $('#s-api').hidden = cfg.provider.mode !== 'api';
      $('#s-web').hidden = cfg.provider.mode !== 'web';
    }));
    $$('[data-preset]').forEach((b) => b.addEventListener('click', () => {
      const p = (S.boot.presets || []).find((x) => x.id === b.dataset.preset);
      if (!p) return;
      cfg.provider.preset = p.id;
      cfg.provider.protocol = p.protocol;
      if (p.baseURL) cfg.provider.baseURL = p.baseURL;
      cfg.provider.model = (p.models && p.models[0]) || '';
      cfg.provider.vision = !!p.vision;
      $('#f-base').value = cfg.provider.baseURL;
      $('#f-model').value = cfg.provider.model;
      $('#f-vision').checked = cfg.provider.vision;
      $$('[data-preset]').forEach((x) => x.classList.toggle('active', x === b));
    }));
    $$('[data-site]').forEach((b) => b.addEventListener('click', () => {
      const s = (S.boot.webSites || []).find((x) => x.id === b.dataset.site);
      if (!s) return;
      cfg.webAI.site = s.id;
      cfg.webAI.url = s.url;
      cfg.webAI.selectors = s.selectors || {};
      $('#f-weburl').value = s.url;
      $$('[data-site]').forEach((x) => x.classList.toggle('active', x === b));
    }));
    $('#f-test')?.addEventListener('click', async () => {
      readTab('model');
      const box = $('#f-test-result');
      box.hidden = false;
      box.className = 'test-result';
      box.textContent = '连接中…';
      try {
        const d = await api.post('/api/provider/test', { config: cfg });
        box.className = 'test-result ' + (d.ok ? 'ok' : 'err');
        box.textContent = d.ok ? `连接成功（${d.latencyMs} ms）\n${d.model}\n${d.reply}` : '失败：' + (d.error || '');
      } catch (e) { box.className = 'test-result err'; box.textContent = e.message; }
    });
    $('#f-webopen')?.addEventListener('click', async () => {
      try {
        const d = await api.post('/api/webai/login');
        $('#f-webstatus').textContent = d.message || d.error || '';
      } catch (e) { toast(e.message, 'err'); }
    });
    $('#f-webcheck')?.addEventListener('click', async () => {
      $('#f-webstatus').textContent = '检测中…';
      try {
        const d = await api.post('/api/webai/check');
        $('#f-webstatus').textContent = d.message;
      } catch (e) { $('#f-webstatus').textContent = e.message; }
    });
  }

  if (name === 'asr') {
    $('#f-asrmode')?.addEventListener('change', (e) => {
      cfg.asr.mode = e.target.value;
      $('#f-asr-api').hidden = e.target.value !== 'api';
      $('#f-asr-local').hidden = e.target.value !== 'local';
    });
    $('#f-mictest')?.addEventListener('click', async () => {
      const box = $('#f-mic-result');
      box.hidden = false;
      box.className = 'test-result';
      box.textContent = '正在录音 2.5 秒…';
      try {
        const d = await api.post('/api/asr/mic');
        box.className = 'test-result ' + (d.ok ? 'ok' : 'err');
        box.textContent = d.ok ? `${d.message}\n录音设备 ${d.devices} 个` : d.error;
      } catch (e) { box.className = 'test-result err'; box.textContent = e.message; }
    });
  }

  if (name === 'lesson') {
    $$('#f-subjects .chip-toggle').forEach((b) => b.addEventListener('click', () => {
      const s = b.dataset.subject;
      if (cfg.lesson.subjects.includes(s)) {
        cfg.lesson.subjects = cfg.lesson.subjects.filter((x) => x !== s);
        b.classList.remove('on');
      } else {
        cfg.lesson.subjects.push(s);
        b.classList.add('on');
      }
    }));
  }

  if (name === 'integrations') {
    refreshInteg();
    $('#f-cipick')?.addEventListener('click', async () => {
      try {
        const d = await api.get('/api/dialog/file?title=' + encodeURIComponent('选择 ClassIsland.exe') + '&filter=' + encodeURIComponent('程序 (*.exe)|*.exe'));
        if (d.path) { $('#f-ciexe').value = d.path; cfg.integrations.classIsland.exePath = d.path; toast('已选择，保存后生效', 'ok'); }
      } catch (e) { toast(e.message, 'err'); }
    });
    $('#f-iccpick')?.addEventListener('click', async () => {
      try {
        const d = await api.get('/api/dialog/file?title=' + encodeURIComponent('选择 InkCanvasForClass.exe') + '&filter=' + encodeURIComponent('程序 (*.exe)|*.exe'));
        if (d.path) { $('#f-iccexe').value = d.path; cfg.integrations.icc.exePath = d.path; toast('已选择，保存后生效', 'ok'); }
      } catch (e) { toast(e.message, 'err'); }
    });
    $('#f-cireload')?.addEventListener('click', refreshInteg);
    refreshCIPlugin();
    $('#f-ciinstall')?.addEventListener('click', async () => {
      try {
        const d = await api.post('/api/integrations/classisland/install', {});
        toast(d.plugin?.installed ? '插件已安装，重启 ClassIsland 后生效' : '已写入插件文件', 'ok');
        refreshCIPlugin();
      } catch (e) { toast(e.message, 'err'); }
    });
    $('#f-ciuninstall')?.addEventListener('click', async () => {
      if (!confirm('从 ClassIsland 卸载 ClassNote 联动插件？')) return;
      try { await api.post('/api/integrations/classisland/uninstall', {}); toast('已卸载', 'ok'); refreshCIPlugin(); }
      catch (e) { toast(e.message, 'err'); }
    });
    $('#f-citest')?.addEventListener('click', async () => {
      try {
        const d = await api.get('/api/integrations/classisland/plugin');
        if (d.running) toast(`已连接 · ${d.state?.inClass ? '上课中 ' + d.state.subject : '当前无课'}`, 'ok');
        else toast(d.error || '插件未运行', 'err');
        refreshCIPlugin();
      } catch (e) { toast(e.message, 'err'); }
    });
    $('#f-cinotify')?.addEventListener('click', async () => {
      try {
        await api.post('/api/integrations/classisland/notify', { title: 'ClassNote', body: '这是一条来自课记的测试提醒。' });
        toast('已在 ClassIsland 弹出提醒', 'ok');
      } catch (e) { toast(e.message, 'err'); }
    });
    $('#f-iccinject')?.addEventListener('click', async () => {
      readTab('integrations');
      try {
        const d = await api.post('/api/integrations/icc/inject', {});
        toast(d.ok ? (d.message || '已注入') : (d.error || '注入失败'), d.ok ? 'ok' : 'err');
        refreshInteg();
      } catch (e) { toast(e.message, 'err'); }
    });
    $('#f-icceject')?.addEventListener('click', async () => {
      try { await api.post('/api/integrations/icc/eject', {}); toast('已移除', 'ok'); refreshInteg(); }
      catch (e) { toast(e.message, 'err'); }
    });
  }

  if (name === 'appearance') {
    $$('[data-theme]').forEach((b) => b.addEventListener('click', () => {
      const t = b.dataset.theme;
      cfg.ui.theme = t;
      document.documentElement.dataset.theme = t;
      const theme = (S.boot.themes || []).find((x) => x.id === t);
      $('#theme-name').textContent = theme ? theme.name : '';
      $$('[data-theme]').forEach((x) => x.classList.toggle('active', x === b));
    }));
  }

  if (name === 'about') {
    $$('[data-open]').forEach((b) => b.addEventListener('click', async () => {
      try { await api.post('/api/open', { target: b.dataset.open }); }
      catch (e) { toast(e.message, 'err'); }
    }));
    $('#f-reset')?.addEventListener('click', async () => {
      if (!confirm('重置所有设置？课堂记录不会被删除。')) return;
      try { await api.post('/api/config/reset', {}); location.reload(); }
      catch (e) { toast(e.message, 'err'); }
    });
    $('#f-quit')?.addEventListener('click', async () => {
      if (!confirm('退出 ClassNote？')) return;
      await api.post('/api/shutdown', {});
      document.body.innerHTML = '<div style="padding:60px;font-family:sans-serif;color:#666">ClassNote 已退出，可以关闭此页面。</div>';
    });
  }
}

async function refreshCIPlugin() {
  const set = (sel, txt) => { const n = $(sel); if (n) n.textContent = txt; };
  try {
    const d = await api.get('/api/integrations/classisland/plugin');
    set('#f-ciplugin', d.installed ? `已安装 ${d.version || ''}` : '未安装');
    set('#f-ciplugindir', d.pluginsDir || '—');
    if (d.running) {
      const st = d.state || {};
      set('#f-cipluginlink', `已连接 · 端口 ${d.port}${st.inClass ? ' · 上课中 ' + (st.subject || '') : ' · 当前无课'}`);
    } else {
      set('#f-cipluginlink', d.installed ? '未运行（重启 ClassIsland 后加载）' : '—');
    }
  } catch (e) {
    set('#f-ciplugin', '检测失败：' + e.message);
  }
}

async function refreshInteg() {
  try {
    const d = await api.get('/api/integrations/status');
    if ($('#f-cistate')) {
      const ci = d.classIsland;
      $('#f-cistate').textContent = ci.detected
        ? (ci.inClass ? `上课中 · ${ci.current?.subject || ''}` : '已读取课表 · 当前无课')
        : '未检测到';
      $('#f-cifile').textContent = ci.profile || '—';
    }
    if ($('#f-iccstate')) {
      const icc = d.icc;
      $('#f-iccstate').textContent = icc.running ? `运行中 · ${icc.windowTitle || ''}` : '未运行';
      $('#f-iccplugin').textContent = icc.pluginInstalled ? `已安装（${icc.pluginVersion}）` : '未安装';
    }
  } catch (e) { /* ignore */ }
  refreshCIPlugin();
}

function readTab(name) {
  const cfg = S.cfg;
  const val = (sel) => { const n = $(sel); return n ? n.value.trim() : ''; };
  const chk = (sel) => { const n = $(sel); return n ? n.checked : false; };

  if (name === 'model') {
    cfg.provider.baseURL = val('#f-base') || cfg.provider.baseURL;
    cfg.provider.model = val('#f-model') || cfg.provider.model;
    const k = val('#f-key');
    if (k && !k.startsWith('•')) cfg.provider.apiKey = k;
    const t = parseFloat(val('#f-temp'));
    if (!Number.isNaN(t)) cfg.provider.temperature = t;
    const mt = parseInt(val('#f-maxtok'), 10);
    if (!Number.isNaN(mt)) cfg.provider.maxTokens = mt;
    cfg.provider.vision = chk('#f-vision');
    cfg.webAI.url = val('#f-weburl') || cfg.webAI.url;
    cfg.webAI.headless = chk('#f-headless');
  }
  if (name === 'asr') {
    cfg.asr.mode = val('#f-asrmode') || cfg.asr.mode;
    cfg.asr.baseURL = val('#f-asrbase') || cfg.asr.baseURL;
    cfg.asr.model = val('#f-asrmodel') || cfg.asr.model;
    const k = val('#f-asrkey');
    if (k && !k.startsWith('•')) cfg.asr.apiKey = k;
    cfg.asr.localExe = val('#f-asexe');
    cfg.asr.localModel = val('#f-asmodel');
    cfg.asr.language = val('#f-aslang') || 'zh';
    const chunk = parseInt(val('#f-aschunk'), 10); if (!Number.isNaN(chunk)) cfg.asr.chunkSec = chunk;
    const sil = parseInt(val('#f-assil'), 10); if (!Number.isNaN(sil)) cfg.asr.silenceMs = sil;
    const th = parseFloat(val('#f-asth')); if (!Number.isNaN(th)) cfg.asr.threshold = th;
  }
  if (name === 'capture') {
    const i = parseInt(val('#f-capint'), 10); if (!Number.isNaN(i)) cfg.capture.intervalMs = i;
    cfg.capture.autoOnSlide = chk('#f-capa');
    cfg.capture.exportHighRes = chk('#f-caph');
    cfg.capture.cropBlackBars = chk('#f-capc');
    const w = parseInt(val('#f-capw'), 10); if (!Number.isNaN(w)) cfg.capture.maxWidth = w;
    const q = parseInt(val('#f-capq'), 10); if (!Number.isNaN(q)) cfg.capture.jpegQuality = q;
    const d = parseInt(val('#f-capd'), 10); if (!Number.isNaN(d)) cfg.capture.diffThreshold = d;
  }
  if (name === 'lesson') {
    cfg.lesson.autoStart = chk('#f-autostart');
    cfg.lesson.autoStop = chk('#f-autostop');
    cfg.lesson.autoReport = chk('#f-autoreport');
    cfg.lesson.analyzeSlides = chk('#f-analyze');
    const text = val('#f-tt');
    const rows = [];
    if (text) {
      for (const line of text.split('\n')) {
        const m = line.trim().match(/^(周[一二三四五六日])\s+(\d{1,2}:\d{2})\s*[-–~]\s*(\d{1,2}:\d{2})\s+(\S+)/);
        if (!m) continue;
        const idx = WEEK.indexOf(m[1]);
        rows.push({ weekday: idx < 0 ? 1 : idx, start: m[2], end: m[3], subject: m[4], room: '' });
      }
    }
    cfg.lesson.manualTimetable = rows;
  }
  if (name === 'integrations') {
    cfg.integrations.classIsland.exePath = val('#f-ciexe');
    cfg.integrations.classIsland.dataDir = val('#f-cidir');
    cfg.integrations.classIsland.enabled = chk('#f-cien');
    cfg.integrations.icc.exePath = val('#f-iccexe');
    cfg.integrations.icc.pluginDir = val('#f-iccdir');
    cfg.integrations.icc.enabled = chk('#f-iccen');
  }
  if (name === 'appearance') {
    const d = $('#f-density');
    if (d) cfg.ui.density = d.value;
  }
}
