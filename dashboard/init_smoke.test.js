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
    storage: { status: 'connected', db_path: '/tmp/usage-keeper.db', db_file_size: 123456 },
  },
};
// Quota endpoints all return {accounts: [...]}; the account shape is wide, so
// exercise the renderer with a couple of representative entries.
const QUOTA_ACCOUNT = { key: 'acct-1', name: 'Account 1', email: 'a@b.c', status: 'active', plan: 'pro', remaining: 5, limit: 10, used: 5, reset_at: iso(-3600e3), models: [] };
['/opencode-quota', '/glmcoding-quota', '/deepseek-quota', '/ollama-quota', '/colab-quota'].forEach(
  (p) => (RESPONSES[p] = { accounts: [QUOTA_ACCOUNT] })
);

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
    const key = Object.keys(RESPONSES).find((k) => path0.includes(k));
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
