// Tests for dashboard themes and theme switcher.
//
// Run with: node --test dashboard/*.test.js
const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const templateHtml = fs.readFileSync(path.join(__dirname, 'template.html'), 'utf8');

test('template.html defines CSS token blocks for all 6 themes', () => {
  const expectedThemeSelectors = [
    ':root, [data-theme="light"]',
    '[data-theme="dark"]',
    '[data-theme="nord"]',
    '[data-theme="dracula"]',
    '[data-theme="sepia"]',
    '[data-theme="emerald"]',
  ];

  for (const selector of expectedThemeSelectors) {
    assert.ok(
      templateHtml.includes(selector),
      `CSS selector ${selector} should be defined in template.html`
    );
  }

  // Ensure critical variables are declared in all themes
  const requiredTokens = [
    '--bg',
    '--surface',
    '--surface-card',
    '--fg',
    '--accent',
    '--chart-1',
    '--chart-2',
    '--chart-3',
  ];
  for (const token of requiredTokens) {
    assert.ok(templateHtml.includes(token), `Token ${token} should exist in CSS`);
  }
});

test('Theme switcher contains dropdown menu and trigger button', () => {
  assert.ok(templateHtml.includes('id="btn-theme"'), 'btn-theme should exist in template');
  assert.ok(templateHtml.includes('id="theme-dropdown-wrap"'), 'theme-dropdown-wrap should exist');
  assert.ok(templateHtml.includes('id="theme-menu-popover"'), 'theme-menu-popover should exist');
  assert.ok(templateHtml.includes('id="theme-btn-label"'), 'theme-btn-label should exist');
});

test('Theme Management script manages state and cycles themes correctly', () => {
  const scriptContent = templateHtml
    .match(/<script>([\s\S]*?)<\/script>/)[1]
    .replace(/%%/g, '%')
    .replace(/%d/g, '0');

  let htmlAttrs = {};
  const rootElement = {
    setAttribute(k, v) { htmlAttrs[k] = String(v); },
    getAttribute(k) { return htmlAttrs[k] || null; },
    removeAttribute(k) { delete htmlAttrs[k]; },
    hasAttribute(k) { return k in htmlAttrs; },
  };

  const store = new Map();
  const elements = new Map();

  function getOrCreateElement(id) {
    if (!elements.has(id)) {
      elements.set(id, {
        id,
        innerHTML: '',
        textContent: '',
        style: {},
        attrs: {},
        remove() {},
        appendChild(n) { return n; },
        querySelectorAll: () => [],
        querySelector: () => null,
        setAttribute(k, v) { this.attrs[k] = String(v); },
        getAttribute(k) { return this.attrs[k] || null; },
      });
    }
    return elements.get(id);
  }

  const sandbox = {
    console,
    document: {
      documentElement: rootElement,
      getElementById: (id) => getOrCreateElement(id),
      createElement: (tag) => getOrCreateElement(tag),
      querySelectorAll: () => [],
      querySelector: () => null,
      addEventListener() {},
      removeEventListener() {},
    },
    fetch: async () => ({ ok: true, status: 200, headers: { get: () => null }, json: async () => ({}), text: async () => '{}' }),
    window: {
      matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
      addEventListener() {},
      removeEventListener() {},
      location: { pathname: '/dashboard' },
    },
    localStorage: {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => store.set(k, String(v)),
      removeItem: (k) => store.delete(k),
      clear: () => store.clear(),
    },
    setTimeout: () => 0,
    clearTimeout() {},
    setInterval: () => 0,
    clearInterval() {},
    showToast() {},
  };

  vm.createContext(sandbox);
  vm.runInContext(scriptContent, sandbox);

  // Assert THEMES array exists and has all 7 entries
  const themes = sandbox.THEMES;
  assert.ok(Array.isArray(themes), 'THEMES should be an array');
  assert.strictEqual(themes.length, 7, 'THEMES should have 6 color themes + 1 auto theme');

  const themeIds = Array.from(themes).map((t) => String(t.id));
  assert.deepStrictEqual(
    themeIds,
    ['light', 'dark', 'nord', 'dracula', 'sepia', 'emerald', 'auto']
  );

  // Test applyTheme
  sandbox.applyTheme('nord', true);
  assert.strictEqual(rootElement.getAttribute('data-theme'), 'nord');
  assert.strictEqual(sandbox.getActiveTheme(), 'nord');
  assert.strictEqual(store.get('theme'), 'nord');

  // Test auto theme removes data-theme attribute
  sandbox.applyTheme('auto', true);
  assert.strictEqual(rootElement.getAttribute('data-theme'), null);
  assert.strictEqual(sandbox.getActiveTheme(), 'auto');

  // Test toggleTheme cycles to next theme
  sandbox.applyTheme('light', true);
  sandbox.toggleTheme();
  assert.strictEqual(sandbox.getActiveTheme(), 'dark');
  sandbox.toggleTheme();
  assert.strictEqual(sandbox.getActiveTheme(), 'nord');

  // Test renderThemeMenuList populates popover
  const popover = getOrCreateElement('theme-menu-popover');
  sandbox.renderThemeMenuList();
  assert.ok(popover.innerHTML.includes('极客冰霜'), 'Popover should contain 极客冰霜');
  assert.ok(popover.innerHTML.includes('Dracula'), 'Popover should contain Dracula');
  assert.ok(popover.innerHTML.includes('Warm Sepia'), 'Popover should contain Warm Sepia');
  assert.ok(popover.innerHTML.includes('Cyber Emerald'), 'Popover should contain Cyber Emerald');
});
