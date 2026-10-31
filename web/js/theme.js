// 主题应用：内置主题走 data-theme 属性，自定义主题注入 CSS 变量。

let styleEl = null;

function injectCustom(vars) {
  if (!styleEl) {
    styleEl = document.createElement('style');
    styleEl.id = 'custom-theme-vars';
    document.head.append(styleEl);
  }
  const css = Object.entries(vars || {})
    .filter(([k, v]) => v)
    .map(([k, v]) => `  --${k}: ${v};`)
    .join('\n');
  styleEl.textContent = css ? `:root{\n${css}\n}` : '';
}

function clearCustom() {
  if (styleEl) styleEl.textContent = '';
}

/**
 * 应用主题。
 * @param {string} name 主题 ID（内置或自定义）
 * @param {Array} customThemes 自定义主题列表（可选，缺省从 S.cfg 读）
 */
export function applyTheme(name, customThemes) {
  const list = customThemes || (window.S && window.S.cfg && window.S.cfg.ui && window.S.cfg.ui.customThemes) || [];
  const custom = list.find((t) => t.id === name);
  if (custom) {
    // 自定义主题：基色沿用 claude，再叠加用户变量
    document.documentElement.dataset.theme = 'claude';
    injectCustom(custom.vars);
    return custom.name || name;
  }
  clearCustom();
  document.documentElement.dataset.theme = name || 'claude';
  return name;
}

// 主题名显示
export function themeLabel(name, builtins, customThemes) {
  const b = (builtins || []).find((t) => t.id === name);
  if (b) return b.name;
  const c = (customThemes || []).find((t) => t.id === name);
  return c ? c.name : '羊皮纸';
}
