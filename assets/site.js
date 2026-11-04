/* ==========================================================================
   ClassNote 官网 · 主题切换
   2026-11

   站点是 Next.js 静态导出，页面主体由 React 渲染。这里刻意不去碰 React
   的组件树：按钮和面板都在 hydration 之后挂到 body 末尾，React 不会回收
   自己没渲染过的兄弟节点，所以刷新、前进后退都不会丢。

   主题本身走 html[data-skin]，配色写在 site.css 里。必须用 !important
   才能盖过 React 在 documentElement 上写的内联 --bg/--ink/...（它自带的
   那套 "设计方向" 面板我们已经在 CSS 里隐藏了）。
   ========================================================================== */
(function () {
  'use strict';

  var STORE_KEY = 'cn-skin';

  var SKINS = [
    {
      id: 'classic',
      name: '经典炭黑',
      sub: 'campus',
      dots: ['#1c1c1c', '#262626', '#8fa68e'],
      dark: true
    },
    {
      id: 'paper',
      name: '纸白',
      sub: 'daylight',
      dots: ['#f4f1ea', '#fbf9f4', '#b0532e'],
      dark: false
    },
    {
      id: 'dsh',
      name: 'DSH 深色',
      sub: 'harness dark',
      dots: ['#0e100f', '#161a18', '#7fa88c'],
      dark: true
    },
    {
      id: 'dsh-light',
      name: 'DSH 浅色',
      sub: 'harness light',
      dots: ['#f2f2ef', '#fbfbf9', '#4a7259'],
      dark: false
    }
  ];

  var byId = {};
  for (var i = 0; i < SKINS.length; i++) byId[SKINS[i].id] = SKINS[i];

  function readStored() {
    try {
      var v = localStorage.getItem(STORE_KEY);
      return byId[v] ? v : '';
    } catch (e) {
      return '';
    }
  }

  function apply(id, persist) {
    var skin = byId[id] || byId.classic;
    var root = document.documentElement;
    root.setAttribute('data-skin', skin.id);
    root.style.colorScheme = skin.dark ? 'dark' : 'light';
    if (persist) {
      try {
        localStorage.setItem(STORE_KEY, skin.id);
      } catch (e) {
        /* 隐私模式下写不进去，不影响本次切换 */
      }
    }
    var items = document.querySelectorAll('.skin-item');
    for (var j = 0; j < items.length; j++) {
      items[j].setAttribute(
        'aria-pressed',
        items[j].getAttribute('data-skin') === skin.id ? 'true' : 'false'
      );
    }
  }

  function iconToggle() {
    return (
      '<svg width="19" height="19" viewBox="0 0 24 24" fill="none" aria-hidden="true">' +
      '<circle cx="12" cy="12" r="8.6" stroke="currentColor" stroke-width="1.5"/>' +
      '<path d="M12 3.4a8.6 8.6 0 0 1 0 17.2z" fill="currentColor"/>' +
      '</svg>'
    );
  }

  function iconCheck() {
    return (
      '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">' +
      '<path d="M4 12.5l5.4 5.4L20 6.6" stroke="currentColor" stroke-width="2.3" ' +
      'stroke-linecap="round" stroke-linejoin="round"/></svg>'
    );
  }

  function build() {
    var wrap = document.createElement('div');
    wrap.className = 'skin-wrap';

    var items = SKINS.map(function (s) {
      var dots = s.dots
        .map(function (c) {
          return '<i style="background:' + c + '"></i>';
        })
        .join('');
      return (
        '<button type="button" class="skin-item" data-skin="' + s.id + '" ' +
        'aria-pressed="false">' +
        '<span class="skin-dots" aria-hidden="true">' + dots + '</span>' +
        '<span><span class="skin-name">' + s.name + '</span>' +
        '<span class="skin-sub">' + s.sub + '</span></span>' +
        '<span class="skin-check">' + iconCheck() + '</span>' +
        '</button>'
      );
    }).join('');

    wrap.innerHTML =
      '<div class="skin-panel" id="skin-panel" data-open="false" role="dialog" ' +
      'aria-label="外观" aria-modal="false">' +
      '<p class="skin-title">外观 / appearance</p>' +
      '<div class="skin-list" role="radiogroup" aria-label="配色">' + items + '</div>' +
      '</div>' +
      '<button type="button" class="skin-btn" id="skin-btn" aria-expanded="false" ' +
      'aria-controls="skin-panel" aria-label="切换外观" title="切换外观">' +
      iconToggle() +
      '</button>';

    document.body.appendChild(wrap);
    return wrap;
  }

  function init() {
    if (document.querySelector('.skin-wrap')) return;

    var wrap = build();
    var btn = wrap.querySelector('.skin-btn');
    var panel = wrap.querySelector('.skin-panel');
    var list = wrap.querySelector('.skin-list');
    var items = [].slice.call(wrap.querySelectorAll('.skin-item'));

    apply(readStored() || 'classic', false);

    function open() {
      panel.setAttribute('data-open', 'true');
      btn.setAttribute('aria-expanded', 'true');
      var current = list.querySelector('.skin-item[aria-pressed="true"]') || items[0];
      if (current) current.focus({ preventScroll: true });
    }

    function close(returnFocus) {
      panel.setAttribute('data-open', 'false');
      btn.setAttribute('aria-expanded', 'false');
      if (returnFocus) btn.focus({ preventScroll: true });
    }

    function isOpen() {
      return panel.getAttribute('data-open') === 'true';
    }

    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      if (isOpen()) close(false);
      else open();
    });

    items.forEach(function (item, idx) {
      item.addEventListener('click', function () {
        apply(item.getAttribute('data-skin'), true);
        close(true);
      });
      item.addEventListener('keydown', function (e) {
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
          e.preventDefault();
          var next = (idx + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
          items[next].focus();
        }
      });
    });

    document.addEventListener('click', function (e) {
      if (isOpen() && !wrap.contains(e.target)) close(false);
    });

    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && isOpen()) close(true);
    });

    // 同一个标签页里开多个入口（首页 / 下载页）时保持同步
    window.addEventListener('storage', function (e) {
      if (e.key === STORE_KEY && byId[e.newValue]) apply(e.newValue, false);
    });
  }

  /* ------------------------------------------------------------------
     更新日志里的 GFM 表格

     下载页用的是一个很轻的 Markdown 渲染器：段落、粗体、行内代码都处
     理了，但表格不支持，于是 `| 区域 | 改动 |` 和 `| --- | --- |` 会以
     纯文本形式整段堆在页面上。这里在它渲染完之后把连续的行重建成真表格。

     单元格用 innerHTML 而不是 textContent，是为了保住里面的 <code>。
     ------------------------------------------------------------------ */

  function splitRow(html) {
    var plain = html.replace(/<[^>]*>/g, '').trim();
    if (plain.charAt(0) !== '|' || plain.charAt(plain.length - 1) !== '|') return null;
    var parts = html.split('|');
    if (parts.length < 4) return null;
    return parts.slice(1, -1).map(function (c) {
      return c.trim();
    });
  }

  function isSeparator(row) {
    for (var i = 0; i < row.length; i++) {
      if (!/^:?-{2,}:?$/.test(row[i].replace(/\s+/g, ''))) return false;
    }
    return true;
  }

  function upgradeTables(root) {
    var ps = [].slice.call(root.querySelectorAll('p'));
    var i = 0;

    while (i < ps.length) {
      var p = ps[i];
      var head = p.parentNode ? splitRow(p.innerHTML) : null;
      if (!head) {
        i++;
        continue;
      }

      var block = [head];
      var j = i + 1;
      while (j < ps.length && ps[j].parentNode) {
        var row = splitRow(ps[j].innerHTML);
        if (!row || row.length !== head.length) break;
        block.push(row);
        j++;
      }

      // 至少要有表头、分隔行、一行数据
      if (block.length < 3 || !isSeparator(block[1])) {
        i = j > i ? j : i + 1;
        continue;
      }

      var table = document.createElement('table');
      var thead = document.createElement('thead');
      var htr = document.createElement('tr');
      block[0].forEach(function (cell) {
        var th = document.createElement('th');
        th.innerHTML = cell;
        htr.appendChild(th);
      });
      thead.appendChild(htr);
      table.appendChild(thead);

      var tbody = document.createElement('tbody');
      for (var r = 2; r < block.length; r++) {
        var tr = document.createElement('tr');
        block[r].forEach(function (cell) {
          var td = document.createElement('td');
          td.innerHTML = cell;
          tr.appendChild(td);
        });
        tbody.appendChild(tr);
      }
      table.appendChild(tbody);

      var box = document.createElement('div');
      box.className = 'md-table';
      box.appendChild(table);
      p.parentNode.replaceChild(box, p);
      for (var k = i + 1; k < j; k++) {
        if (ps[k].parentNode) ps[k].remove();
      }
      i = j;
    }
  }

  function watchChangelog() {
    var box = document.querySelector('div.field--ink > div[style*="overflow:auto"]');
    if (!box) return;

    var running = false;
    function run() {
      if (running) return;
      running = true;
      observer.disconnect();
      upgradeTables(box);
      observer.observe(box, { childList: true, subtree: true });
      running = false;
    }

    var observer = new MutationObserver(run);
    observer.observe(box, { childList: true, subtree: true });
    run();

    /* GitHub 的匿名接口每个 IP 每小时只有 60 次，超了就是 403。
       组件本身没有超时处理，会一直停在"正在读取"。这里补一句兜底，
       等真实内容到达时再自动撤掉。 */
    var hint = document.createElement('p');
    hint.className = 'md-slow';
    hint.innerHTML =
      '读取较慢：GitHub 接口有访问频率限制。可以直接打开 ' +
      '<a href="https://github.com/tangyuan9325/ClassNote/releases" ' +
      'target="_blank" rel="noreferrer">Releases 页</a> 查看完整更新日志。';

    var timer = setTimeout(function () {
      if (/正在读取/.test(box.textContent) && !hint.parentNode) box.appendChild(hint);
    }, 6000);

    var settle = new MutationObserver(function () {
      if (!/正在读取/.test(box.textContent)) {
        clearTimeout(timer);
        if (hint.parentNode) hint.remove();
        settle.disconnect();
      }
    });
    settle.observe(box, { childList: true, subtree: true, characterData: true });
  }

  function boot() {
    init();
    watchChangelog();
  }

  /* 不能等 window.load：线上要拉 5 张截图和 Google Fonts，load 常常拖到
     十秒以后，按钮会迟迟不出现。DOMContentLoaded 时就挂上去，React 的
     hydration 只会 patch 自己渲染的子树，不会回收 body 末尾的兄弟节点；
    万一被回收，load 时再补一次。 */
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot, { once: true });
  } else {
    boot();
  }
  window.addEventListener('load', function () {
    if (!document.querySelector('.skin-wrap')) init();
    if (!document.querySelector('div.field--ink > div[style*="overflow:auto"]')) watchChangelog();
  });
})();
