// 通用工具：DOM、格式化、轻量 Markdown
export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

export function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') node.className = v;
    else if (k === 'html') node.innerHTML = v;
    else if (k === 'text') node.textContent = v;
    else if (k.startsWith('on') && typeof v === 'function') node.addEventListener(k.slice(2), v);
    else if (k === 'dataset') Object.assign(node.dataset, v);
    else node.setAttribute(k, v);
  }
  for (const c of [].concat(children)) {
    if (c === null || c === undefined || c === false) continue;
    node.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return node;
}

export function esc(s = '') {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

let toastTimer = 0;
export function toast(msg, kind = '') {
  const wrap = $('#toasts');
  if (!wrap) return;
  const t = el('div', { class: 'toast ' + kind, text: msg });
  wrap.append(t);
  clearTimeout(toastTimer);
  setTimeout(() => { t.style.opacity = '0'; t.style.transition = 'opacity .3s'; setTimeout(() => t.remove(), 320); }, kind === 'err' ? 5200 : 2800);
}

export function fmtTime(d) {
  const t = new Date(d);
  if (Number.isNaN(t.getTime())) return '';
  return `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`;
}

export function fmtDate(d) {
  const t = new Date(d);
  if (Number.isNaN(t.getTime())) return '';
  return `${t.getMonth() + 1}月${t.getDate()}日`;
}

export function fmtDur(sec) {
  sec = Math.max(0, Math.round(sec || 0));
  if (sec < 60) return `${sec}″`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m}′`;
  return `${Math.floor(m / 60)}h${m % 60}′`;
}

// 极简 Markdown：标题、列表、粗斜体、行内代码、段落
export function md(src = '') {
  const lines = String(src).replace(/\r\n/g, '\n').split('\n');
  let html = '';
  let list = null;
  const inline = (s) => esc(s)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(?<!\*)\*([^*]+)\*(?!\*)/g, '<em>$1</em>');

  const closeList = () => { if (list) { html += `</${list}>`; list = null; } };

  for (const raw of lines) {
    const line = raw.trimEnd();
    if (!line.trim()) { closeList(); continue; }
    let m;
    if ((m = line.match(/^(#{1,4})\s+(.*)$/))) {
      closeList();
      const level = Math.min(4, m[1].length + 2);
      html += `<h${level}>${inline(m[2])}</h${level}>`;
    } else if ((m = line.match(/^\s*[-*+]\s+(.*)$/))) {
      if (list !== 'ul') { closeList(); html += '<ul>'; list = 'ul'; }
      html += `<li>${inline(m[1])}</li>`;
    } else if ((m = line.match(/^\s*\d+[.、]\s+(.*)$/))) {
      if (list !== 'ol') { closeList(); html += '<ol>'; list = 'ol'; }
      html += `<li>${inline(m[1])}</li>`;
    } else if ((m = line.match(/^>\s*(.*)$/))) {
      closeList();
      html += `<blockquote>${inline(m[1])}</blockquote>`;
    } else {
      closeList();
      html += `<p>${inline(line)}</p>`;
    }
  }
  closeList();
  return html;
}

export function debounce(fn, ms = 200) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

export function copyImage(url) {
  const img = $('#lightbox-img');
  if (img) { img.src = url; $('#lightbox').hidden = false; }
}
