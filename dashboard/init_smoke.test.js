// Whole-page smoke test: runs the dashboard's real <script> in a stubbed DOM,
// then renders all six tabs and asserts none of them threw or produced a
// broken panel. This is the regression net for "one bad field access blanks a
// whole card" bugs (v0.11.12 shipped a Top-5 card that threw a TypeError before
// it ever reached innerHTML).
//
// Run with: node --test dashboard/*.test.js
const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs
  .readFileSync(path.join(__dirname, 'template.html'), 'utf8')
  .match(/<script>([\s\S]*?)<\/script>/)[1]
  .replace(/%%/g, '%')
  .replace(/%d/g, '0');

// ── Canned API responses ────────────────────────────────────────────────────
const now = Date.now();
const iso = (offsetMs) => new Date(now - offsetMs).toISOString();
const bucket = (i) => ({
  start: iso(i * 2 * 3600e3),
  end: iso((i + 1) * 2 * 3600e3),
  label: 'b' + i,
  requests: 10 + i,
  tokens: 1000 + i,
  input_tokens: 800,
  output_tokens: 200,
  cached_tokens: 100,
  cost: 1.25 + i,
});
const event = (over) =>
  Object.assign(
    {
      id: 1,
      timestamp: iso(60000),
      provider: 'test-provider',
      model: 'gpt-5.6-terra',
      executor_type: 'APIExecutor',
      input_tokens: 120,
      output_tokens: 60,
      reasoning_tokens: 10,
      total_tokens: 180,
      cached_tokens: 20,
      cache_read_tokens: 20,
      cache_creation_tokens: 0,
      latency_ms: 240,
      ttft_ms: 40,
      cache_hit_rate: 14,
      failed: false,
      failure_status_code: 0,
      failure_body: '',
      auth_id: 'auth-1',
      source: 'api',
      service_tier: 'default',
    },
    over
  );

const RESPONSES = {
  '/summary': {
    total_requests: 42,
    total_tokens: 9000,
    input_tokens: 7000,
    output_tokens: 2000,
    failed_requests: 2,
    unique_models: 3,
    avg_latency_ms: 210,
    cache_hit_rate: 55,
    range_hours: 720,
  },
  '/timeseries': { range_hours: 720, buckets: 12, series: Array.from({ length: 12 }, (_, i) => bucket(i)) },
  '/models': [
    { model: 'gpt-5.6-terra', provider: 'p1', requests: 30, input_tokens: 5000, output_tokens: 1200, total_tokens: 6200, cached_tokens: 300, cost: 12.5 },
    { model: 'gemini-3.5-flash', provider: 'p2', requests: 10, input_tokens: 1500, output_tokens: 600, total_tokens: 2100, cached_tokens: 100, cost: 3.25 },
    { model: 'deepseek-v4-flash', provider: 'p3', requests: 2, input_tokens: 400, output_tokens: 300, total_tokens: 700, cached_tokens: 0, cost: 0.5 },
  ],
  '/events': {
    total: 2,
    limit: 50,
    offset: 0,
    events: [event({}), event({ id: 2, model: 'gemini-3.5-flash', failed: true, failure_status_code: 500, failure_body: 'upstream exploded' })],
  },
  '/prices': {
    prices: {
      'gpt-5.6-terra': { prompt: 1, completion: 2, cache: 0.1, auto_synced: true },
      'gemini-3.5-flash': { prompt: 0.3, completion: 2.5, cache: 0.075, auto_synced: true },
    },
  },
  '/health': {
    status: 'ok',
    alerts: [],
    runtime: {
      uptime_seconds: 7200,
      write_queue_used: 0,
      write_queue_size: 2500,
      last_write_duration_ms: 3,
      storage_write_errors: 0,
      summary_cache_hit_rate: 66,
      events_cache_hit_rate: 66,
      total_requests: 120,
      dropped_usage_events: 0,
      plugin_panics: 0,
      price_sync: '2026-10-04T04:00:00Z',
      unpriced_models: 0,
      recent_errors: [{ time: '2026-10-04T04:00:00Z', code: 'price_sync_failed', message: 'timeout' }],
    },
    storage: { status: 'connected', db_path: '/tmp/usage-keeper.db', db_file_size: 123456, integrity_ok: true },
  },
};
// Quota endpoints all return {accounts: [...]}; the account shape is wide, so
// exercise the renderer with a couple of representative entries.
const QUOTA_ACCOUNT = { key: 'acct-1', name: 'Account 1', email: 'a@b.c', status: 'active', plan: 'pro', remaining: 5, limit: 10, used: 5, reset_at: iso(-3600e3), models: [] };
['/opencode-quota', '/glmcoding-quota', '/deepseek-quota', '/ollama-quota', '/colab-quota'].forEach(
  (p) => (RESPONSES[p] = { accounts: [QUOTA_ACCOUNT] })
);
// NewAPI/dddai uses a different window shape: a subscription progress bar, a
// wallet balance without a usable total, and a text-only burn-rate estimate.
RESPONSES['/newapi-quota'] = {
  accounts: [
    {
      type: 'newapi',
      name: 'dddai-main',
      plan: '增强月卡',
      success: true,
      updated_at: iso(0),
      base_url: 'https://dddai.dev',
      windows: [
        { label: '增强月卡 · sub-openai', used: 235.97, total: 950, remaining: 714.03, unit: 'USD', status_text: '剩余 $714.03 · 11 天后到期 · 面板显示 ¥4855.20' },
        { label: 'Claude Kiro 周卡 · sub-claude-kiro', used: 200, total: 720, remaining: 520, unit: 'USD', status_text: '剩余 $520.00 · 3 天后到期' },
        { label: '钱包余额', used: 0, total: 0, remaining: 0.6, unit: 'USD', status_text: '累计已用 $1136.59（历史累计）' },
        { label: '用量预估', used: 0, total: 0, remaining: 0, unit: 'USD/天', display: 'text', status_text: '近 7 天日均 $11.85 · 合计剩余 $1234.63 ≈ 104.2 天（各订阅额度不互通）' },
      ],
    },
  ],
};

// ── Minimal DOM stub ────────────────────────────────────────────────────────
// Records every non-empty innerHTML write so a render can be checked without
// hard-coding which sub-container each tab happens to fill.
const writes = [];
function makeElement(tag) {
  const el = {
    tagName: String(tag || 'div').toUpperCase(),
    style: {},
    dataset: {},
    _attrs: {},
    value: '',
    disabled: false,
    checked: false,
    innerHTML: '',
    textContent: '',
    scrollTop: 0,
    children: [],
    classList: {
      _set: new Set(),
      add(...c) { c.forEach((x) => this._set.add(x)); },
      remove(...c) { c.forEach((x) => this._set.delete(x)); },
      contains(c) { return this._set.has(c); },
      toggle(c, force) { const on = force === undefined ? !this._set.has(c) : !!force; on ? this._set.add(c) : this._set.delete(c); return on; },
    },
  };
  const ctx2d = {
    canvas: { width: 600, height: 200 },
    clearRect() {}, fillRect() {}, strokeRect() {}, beginPath() {}, closePath() {}, moveTo() {}, lineTo() {},
    arc() {}, rect() {}, fill() {}, stroke() {}, save() {}, restore() {}, translate() {}, scale() {}, rotate() {},
    setLineDash() {}, fillText() {}, strokeText() {}, drawImage() {}, createLinearGradient: () => ({ addColorStop() {} }),
    measureText: () => ({ width: 10 }), toDataURL: () => 'data:,', getImageData: () => ({ data: [] }), putImageData() {},
  };
  const methods = {
    appendChild: (n) => n, insertBefore: (n) => n, removeChild: (n) => n, replaceChildren() {},
    addEventListener() {}, removeEventListener() {}, dispatchEvent() {}, focus() {}, blur() {}, click() {}, remove() {},
    getAttribute: (n) => (n in el._attrs ? el._attrs[n] : null),
    setAttribute: (n, v) => { el._attrs[n] = String(v); },
    removeAttribute: (n) => { delete el._attrs[n]; },
    hasAttribute: (n) => n in el._attrs,
    querySelector: () => null,
    querySelectorAll: () => [],
    closest: () => null,
    contains: () => false,
    getBoundingClientRect: () => ({ width: 600, height: 200, top: 0, left: 0, right: 600, bottom: 200, x: 0, y: 0 }),
    getContext: () => ctx2d,
    scrollIntoView() {}, setPointerCapture() {}, releasePointerCapture() {},
    animate: () => ({ cancel() {}, finished: Promise.resolve() }),
    insertAdjacentHTML() {},
  };
  return new Proxy(el, {
    get(t, prop) {
      if (prop in t) return t[prop];
      if (prop in methods) return methods[prop];
      if (prop === 'firstChild' || prop === 'lastChild' || prop === 'parentNode' || prop === 'nextSibling') return null;
      if (prop === 'offsetWidth' || prop === 'clientWidth' || prop === 'scrollWidth') return 600;
      if (prop === 'offsetHeight' || prop === 'clientHeight' || prop === 'scrollHeight') return 200;
      if (prop === 'offsetLeft' || prop === 'offsetTop') return 0;
      if (prop === 'ownerDocument') return documentStub;
      return undefined;
    },
    set(t, prop, val) {
      t[prop] = val;
      if (prop === 'innerHTML' && typeof val === 'string' && val.length > 0) {
        writes.push({ id: t.id || t.tagName, html: val });
      }
      return true;
    },
  });
}

const byId = new Map();
const documentStub = {
  getElementById(id) {
    if (!byId.has(id)) {
      const el = makeElement('div');
      el.id = id;
      byId.set(id, el);
    }
    return byId.get(id);
  },
  createElement: (tag) => makeElement(tag),
  createElementNS: (ns, tag) => makeElement(tag),
  createTextNode: (t) => ({ textContent: t }),
  querySelector: () => null,
  querySelectorAll: () => [],
  addEventListener() {}, removeEventListener() {},
  body: makeElement('body'),
  documentElement: makeElement('html'),
  head: makeElement('head'),
  hidden: false,
  visibilityState: 'visible',
  cookie: '',
};

const store = new Map();
const sandbox = {
  console,
  document: documentStub,
  window: {
    addEventListener() {}, removeEventListener() {}, matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    location: { href: 'http://localhost/', pathname: '/v0/resource/plugins/usage-keeper/dashboard', search: '', hash: '', reload() {} },
    innerWidth: 1440, innerHeight: 900, devicePixelRatio: 2,
    localStorage: null, open: () => null, print() {}, getComputedStyle: () => ({ getPropertyValue: () => '' }),
  },
  localStorage: {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
    clear: () => store.clear(),
  },
  navigator: { userAgent: 'node', clipboard: { writeText: async () => {} }, language: 'zh-CN' },
  // Timers are inert: the smoke test drives renders explicitly, and we do not
  // want the periodic refresh or toast timers keeping the process alive.
  setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
  requestAnimationFrame: () => 0, cancelAnimationFrame() {},
  fetch: async (url) => {
    const path0 = String(url);
    // Prefer the most specific match: "/x?action=keys" must win over "/x".
    const key = Object.keys(RESPONSES)
      .filter((k) => path0.includes(k))
      .sort((a, b) => b.length - a.length)[0];
    if (!key) throw new Error('smoke-stub: unexpected request ' + path0);
    const data = RESPONSES[key];
    return {
      ok: true,
      status: 200,
      headers: { get: () => null },
      json: async () => JSON.parse(JSON.stringify(data)),
      text: async () => JSON.stringify(data),
    };
  },
  AbortController: class { constructor() { this.signal = {}; } abort() {} },
  URL: { createObjectURL: () => 'blob:x', revokeObjectURL() {} },
  Blob: class {}, Image: class {}, btoa: (s) => Buffer.from(s, 'binary').toString('base64'),
  atob: (s) => Buffer.from(s, 'base64').toString('binary'),
  encodeURIComponent, decodeURIComponent, Date, Math, JSON, Number, String, Boolean, Array, Object, RegExp, Error, Promise, Set, Map, parseInt, parseFloat, isNaN, isFinite,
};
sandbox.window.localStorage = sandbox.localStorage;
sandbox.globalThis = sandbox;
vm.createContext(sandbox);

// ── Tests ───────────────────────────────────────────────────────────────────
test('dashboard script initialises without throwing', () => {
  assert.doesNotThrow(() => vm.runInContext(source, sandbox, { filename: 'dashboard-script.js' }));
});

RESPONSES['/newapi-quota?action=resetkey'] = { ok: true, key_id: 1 };
RESPONSES['/newapi-quota?action=keys'] = {
  account: 'dddai-main',
  total: 3,
  quota_per_unit: 500000,
  display_type: 'CNY',
  exchange_rate: 6.8,
  keys: [
    { id: 1, name: 'prod-key', status: '启用', group: 'sub-openai', used_usd: 65, remaining_usd: 15, remaining_quota: 7500000, total_usd: 80,
      cycle_active: true, cycle_total_usd: 20, cycle_used_usd: 5, cycle_reset_at: '2026-10-21T02:25:00Z', panel_remaining: '¥102.00',
      window: { used: 5, remaining: 15, total: 20, unit: 'USD', status_text: '本轮已用 $5.00 / $20.00 · 余 $15.00' } },
    { id: 2, name: 'unlimited-key', status: '启用', unlimited: true, used_usd: 2, remaining_usd: 0, total_usd: 0, window: { used: 2, remaining: 0, total: 0, unit: 'USD', unlimited: true, status_text: '不限量' } },
    { id: 3, name: 'retired-key', status: '已禁用', used_usd: 0, remaining_usd: 0, total_usd: 0, window: { used: 0, remaining: 0, total: 0, unit: 'USD', status_text: '已用 $0.00 · 余 $0.00' } },
  ],
};

test('models tab shows a real error instead of an empty state on query failure', async () => {
  // A failed backend query must not be rendered as "no usage records" — that
  // ambiguity hid a corrupted SQLite index in production.
  const originalFetch = sandbox.fetch;
  sandbox.fetch = async (url) => {
    if (String(url).includes('/models')) throw new Error('HTTP 500');
    return originalFetch(url);
  };
  sandbox.lastModelsData = null;
  writes.length = 0;
  await sandbox.renderModels();
  sandbox.fetch = originalFetch;
  sandbox.lastModelsData = null;

  const html = writes.map((w) => w.html).join('\n');
  assert.ok(html.includes('模型用量查询失败'), 'failure state missing');
  assert.ok(!html.includes('当前时间段暂无模型用量记录'), 'failure rendered as an empty range');
});

test('newapi key rows offer one-click reset only for limited keys', async () => {
  writes.length = 0;
  sandbox.openNewAPIKeys('dddai-main');
  await new Promise((resolve) => setImmediate(resolve));
  const html = writes.map((w) => w.html).join('\n');

  // Exactly one reset action, bound to the limited key's id. Splitting on the
  // key name is unreliable because the same name also appears inside the
  // generated onclick attribute.
  const actions = html.match(/openNewAPIResetKey\(([^)]*)\)/g) || [];
  assert.strictEqual(actions.length, 2, 'expected reset actions only for non-unlimited keys: ' + JSON.stringify(actions));
  assert.ok(actions.every((a) => a.includes("'prod-key'") || a.includes("'retired-key'")), actions.join(','));
  assert.ok(!actions.some((a) => a.includes("'unlimited-key'")), 'unlimited key must not offer reset');
  assert.ok(html.includes('重置额度'), 'reset label missing');
  // The action belongs to the key's header row, right-aligned, so every entry
  // starts at the same x and the button cannot be mistaken for another key's.
  const header = html.split('align-items:center;gap:12px')[1] || '';
  assert.ok(header.includes('重置额度'), 'reset action is not in the header row');
  assert.ok(!html.includes('justify-content:flex-end;margin-top:8px'), 'action still on a stray row');
});

test('newapi reset dialog asks for confirmation and blocks invalid input', async () => {
  writes.length = 0;
  // Prime the panel factors the way openNewAPIKeys() does.
  sandbox.newAPIPanelFactors = { unit: 500000, displayType: 'CNY', rate: 6.8 };
  sandbox.openNewAPIResetKey('dddai-main', 1, 'prod-key', 5, 15, 7500000);
  const body = sandbox.document.getElementById('newapi-reset-body');
  assert.ok(body.innerHTML.includes('prod-key'), 'dialog does not name the key');
  assert.ok(body.innerHTML.includes('只修改「剩余配额」这一个字段'), 'dialog omits the single-field promise');
  assert.ok(body.innerHTML.includes('name="' ) === false);

  // Zero/negative values must not issue a request.
  const amount = sandbox.document.getElementById('newapi-reset-amount');
  const calls = [];
  const originalFetch = sandbox.fetch;
  sandbox.fetch = async (url) => { calls.push(String(url)); return originalFetch(url); };
  amount.value = '0';
  sandbox.doNewAPIResetKey();
  amount.value = '-3';
  sandbox.doNewAPIResetKey();
  sandbox.fetch = originalFetch;
  assert.strictEqual(calls.length, 0, 'invalid amounts must not hit the API');

  sandbox.closeNewAPIReset();
});

test('reset dialog stacks above the key list it is opened from', () => {
  // Both dialogs default to z-index 2500; equal values resolve by DOM order and
  // the keys list comes later, so the reset confirmation was painted underneath
  // and its button could not be clicked (no request ever reached the backend).
  const html = fs.readFileSync(path.join(__dirname, 'template.html'), 'utf8');
  const resetTag = html.split('<div')[html.split('<div').findIndex((t) => t.includes('id="newapi-reset-overlay"'))];
  assert.ok(resetTag.includes('modal-overlay-stacked'), 'reset overlay is not marked as stacked');
  const rule = html.match(/\.modal-overlay-stacked\{[^}]*\}/);
  assert.ok(rule, 'stacked overlay rule missing');
  const z = Number((rule[0].match(/z-index:(\d+)/) || [])[1]);
  const base = Number((html.match(/\.modal-overlay\{[^}]*z-index:(\d+)/) || [])[1]);
  assert.ok(z > base, 'stacked overlay must outrank the base overlay: ' + z + ' vs ' + base);
});

test('reset dialog prefills the exact stored quota and shows the conversion', () => {
  sandbox.newAPIPanelFactors = { unit: 500000, displayType: 'CNY', rate: 6.8 };
  // 50551764 units is $101.103528; a 2-decimal prefill would silently reset to
  // $101.10 and lose 1,764 units.
  sandbox.openNewAPIResetKey('acct', 9876, 'team', 102.99, 101.103528, 50551764);

  // The DOM stub does not parse innerHTML into child nodes, so assert on the
  // emitted markup rather than on a re-queried input element.
  const html = sandbox.document.getElementById('newapi-reset-body').innerHTML;
  assert.ok(html.includes('value="101.103528"'), 'prefill rounded the stored quota: ' + html.slice(0, 200));
  assert.ok(html.includes('500,000'), 'dialog omits the quota unit');
  assert.ok(html.includes('¥687.50'), 'dialog omits the panel currency equivalent');

  // Round-trip: confirming the untouched prefill must reproduce the counter.
  assert.strictEqual(sandbox.fmtExact(50551764 / 500000), '101.103528');
  const c = sandbox.convertQuotaPreview(50551764 / 500000);
  assert.strictEqual(c.internal, 50551764, 'prefill does not round-trip to the stored counter');
  assert.strictEqual(c.panel, '¥687.50');

  // A rounded prefill demonstrably loses units, which is why we do not use it.
  assert.strictEqual(sandbox.convertQuotaPreview(101.10).internal, 50550000);
  sandbox.closeNewAPIReset();
});

test('newapi key list modal renders names and usage without secrets', async () => {
  sandbox.openNewAPIKeys('dddai-main');
  await new Promise((resolve) => setTimeout(resolve, 0));
  // The stubbed fetch resolves on a microtask; flush it.
  await new Promise((resolve) => setImmediate(resolve));

  const html = writes.map((w) => w.html).join('\n');
  assert.ok(html.includes('prod-key'), 'key name missing');
  // Stats render as label/value pairs: 本轮已用 $5.00 / $20.00.
  assert.ok(html.includes('本轮已用'), 'cycle usage label missing');
  assert.ok(html.includes('$5.00 / $20.00'), 'cycle usage values missing');
  assert.ok(html.includes('unlimited-key'), 'unlimited key missing');
  assert.ok(html.includes('retired-key'), 'disabled key missing');
  assert.ok(html.includes('不读取 key 明文'), 'privacy note missing');
  // A reset must restart the bar but never hide lifetime spend. Stats render as
  // separate label/value spans, so match them individually.
  assert.ok(html.includes('累计已用'), 'lifetime usage label hidden after reset');
  assert.ok(html.includes('$65.00'), 'lifetime usage value hidden after reset');
  assert.ok(html.includes('本轮起算 2026-10-21'), 'reset date missing');
  // 5 of the 20 granted this cycle, not 65 of 80 lifetime.
  assert.ok(html.includes('width:25%%') || html.includes('width:25%'), 'bar does not use cycle numbers');
  // The parenthesised amount annotates the balance, so it must be the panel
  // value of the remaining quota, not of lifetime spend.
  assert.ok(html.includes('(¥102.00)'), 'balance annotation missing or wrong currency source');
  assert.ok(!html.includes('(¥442.00)'), 'annotated lifetime spend instead of remaining');
  assert.ok(!/sk-/.test(html), 'rendered a secret-looking value');
});

test('newapi quota card renders subscription, wallet and estimate windows', async () => {
  sandbox.currentQuotaProvider = 'newapi';
  writes.length = 0;
  await assert.doesNotReject(() => sandbox.renderQuota(), 'renderQuota() threw for newapi');

  const html = writes.map((w) => w.html).join('\n');
  assert.ok(html.includes('dddai-main'), 'account name missing');
  assert.ok(html.includes('增强月卡'), 'plan title missing');
  assert.ok(html.includes('$235.97 / $950.00'), 'subscription used/total missing');
  assert.ok(html.includes('$200.00 / $720.00'), 'second subscription bar missing');
  assert.ok(html.includes('Claude Kiro 周卡'), 'second plan label missing');
  assert.ok(html.includes('$0.60'), 'wallet balance missing');
  assert.ok(html.includes('104.2 天'), 'burn-rate estimate missing');
  assert.ok(html.includes('各订阅额度不互通'), 'multi-subscription caveat missing');
  assert.ok(html.includes('https://dddai.dev'), 'base url missing from card');
  assert.ok(html.includes('编辑凭据'), 'edit-credentials action missing');
  assert.ok(!/undefined/.test(html), 'newapi card rendered "undefined"');
  assert.ok(!/NaN/.test(html), 'newapi card rendered "NaN"');
  sandbox.currentQuotaProvider = 'opencode';
});

test('every tab renders content with no template artefacts', async () => {
  // `expect` is a value that must appear in the rendered HTML: esc() maps
  // undefined to "", so "no undefined" alone would not catch a render that
  // silently dropped its data.
  const tabs = [
    // The overview's KPI numbers are set via textContent and its chart labels
    // live in hover tooltips, so assert on the rendered SVG instead.
    ['overview', 'renderOverview', '<svg'],
    ['models', 'renderModels', 'gpt-5.6-terra'],
    ['events', 'renderEvents', 'gpt-5.6-terra'],
    ['quota', 'renderQuota', null],
    ['pricing', 'renderPricing', 'gpt-5.6-terra'],
    ['health', 'renderHealth', '系统运行状态'],
  ];

  for (const [tab, fn, expect] of tabs) {
    assert.strictEqual(typeof sandbox[fn], 'function', fn + ' should be a global function');

    writes.length = 0;
    await assert.doesNotReject(() => sandbox[fn](), fn + '() threw for the ' + tab + ' tab');

    assert.ok(writes.length > 0, tab + ' tab wrote nothing to any container');

    const html = writes.map((w) => w.html).join('\n');
    if (expect) {
      assert.ok(
        html.includes(expect),
        tab + ' tab rendered without ' + JSON.stringify(expect) + ' (data did not reach the DOM)'
      );
    }
    assert.ok(!/undefined/.test(html), tab + ' tab rendered "undefined" into ' + writes.map((w) => w.id).join(', '));
    assert.ok(!/NaN/.test(html), tab + ' tab rendered "NaN" into ' + writes.map((w) => w.id).join(', '));
    assert.ok(!/\[object Object\]/.test(html), tab + ' tab rendered "[object Object]"');
  }
});
