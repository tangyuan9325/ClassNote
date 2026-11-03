// 与本地服务通信
function readCookie(name) {
  const m = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
  return m ? decodeURIComponent(m[1]) : '';
}

export function authToken() {
  return readCookie('cn_token') || localStorage.getItem('cn_token') || '';
}

async function request(path, options = {}) {
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
  const tok = authToken();
  if (tok) headers['X-ClassNote-Token'] = tok;
  const res = await fetch(path, { ...options, headers });
  const type = res.headers.get('content-type') || '';
  const data = type.includes('json') ? await res.json().catch(() => ({})) : await res.text();
  if (!res.ok) {
    const msg = (data && data.error) || (typeof data === 'string' ? data : `请求失败 ${res.status}`);
    // 带上状态码与业务码：调用方要能分辨「输入错了」和「版本不兼容」。
    const err = new Error(msg);
    err.status = res.status;
    err.code = (data && data.code) || '';
    err.data = data;
    err.incompatible = err.code === 'incompatible';
    throw err;
  }
  return data;
}

export const api = {
  get: (p) => request(p),
  post: (p, body = {}) => request(p, { method: 'POST', body: JSON.stringify(body) }),
  del: (p) => request(p, { method: 'DELETE' }),
};

export function connectEvents(handlers) {
  let es;
  try {
    es = new EventSource('/api/events');
  } catch (e) {
    return () => {};
  }
  const types = ['session', 'session_ended', 'slide', 'transcript', 'analysis', 'segment_status',
    'report', 'error', 'class_state', 'auto_start', 'auto_stop', 'icc', 'classisland', 'test',
    'ui_command', 'dsh_log', 'asr_model', 'update_progress'];
  for (const t of types) {
    es.addEventListener(t, (ev) => {
      let payload = {};
      try { payload = JSON.parse(ev.data); } catch (e) { /* ignore */ }
      const h = handlers[t] || handlers['*'];
      if (h) h(payload.data !== undefined ? payload.data : payload, payload);
    });
  }
  es.onerror = () => { /* EventSource 会自动重连 */ };
  return () => es.close();
}
