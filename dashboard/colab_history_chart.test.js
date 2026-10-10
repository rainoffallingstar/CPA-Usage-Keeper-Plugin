// Regression tests for the Colab quota history line chart.
//
// The chart once shipped rendering its axes, grid and legend but *no* data
// line: renderColabHistoryChart appended to chartBox.innerHTML after the
// opening <svg>, which makes the HTML parser re-parse the fragment in the HTML
// namespace. The <path>/<text> nodes then live OUTSIDE the <svg> element and
// never render (the x labels also collapsed into the left edge as inline text).
//
// These tests exercise the real template.html function and assert the whole
// SVG is emitted as one string, with the series path and ticks inside it.
//
// Run with: node --test dashboard/*.test.js
const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const templateSource = fs
  .readFileSync(path.join(__dirname, 'template.html'), 'utf8')
  .replace(/%%/g, '%');

function extractFunction(name) {
  const start = templateSource.indexOf('function ' + name + '(');
  assert.ok(start >= 0, 'function ' + name + ' not found in template.html');
  let depth = 0;
  let i = templateSource.indexOf('{', start);
  for (; i < templateSource.length; i++) {
    if (templateSource[i] === '{') depth++;
    else if (templateSource[i] === '}') {
      depth--;
      if (depth === 0) {
        i++;
        break;
      }
    }
  }
  assert.ok(depth === 0, 'unbalanced braces while extracting ' + name);
  return templateSource.slice(start, i);
}

function makeSandbox() {
  const sandbox = { Math, Number, isFinite, Date, String, Array, console };
  vm.createContext(sandbox);
  vm.runInContext(extractFunction('smoothPath'), sandbox);
  vm.runInContext(extractFunction('renderColabHistoryChart'), sandbox);
  return sandbox;
}

// Render into a stubbed container and return the produced chartBox innerHTML.
function render(sandbox, points, days) {
  const chartBox = { clientWidth: 1150, innerHTML: '' };
  const container = { querySelector: () => chartBox };
  sandbox.renderColabHistoryChart(container, points, days || 30);
  return chartBox.innerHTML;
}

const paidSeries = [
  { ts: '2026-09-25T14:51:37Z', paid_balance: 79.6, free_remaining: 0, has_paid: true, has_free: false },
  { ts: '2026-10-03T00:00:00Z', paid_balance: 150.2, free_remaining: 0, has_paid: true, has_free: false },
  { ts: '2026-10-10T03:25:54Z', paid_balance: 216.1, free_remaining: 0, has_paid: true, has_free: false },
];

test('colab history chart keeps the series path inside the <svg>', () => {
  const html = render(makeSandbox(), paidSeries);
  const svgOpen = html.indexOf('<svg');
  const svgClose = html.indexOf('</svg>');
  const pathIdx = html.indexOf('<path ');

  assert.ok(svgOpen >= 0 && svgClose > svgOpen, 'chart should emit exactly one <svg>');
  assert.strictEqual(html.indexOf('</svg>', svgClose + 1), -1, 'must not emit a second </svg>');
  assert.ok(pathIdx > svgOpen && pathIdx < svgClose, 'series <path> must be inside the <svg>');
  assert.ok(/<path d="M [\d.]+ [\d.]+/.test(html), 'series path must carry real geometry');
  assert.ok(html.includes('付费 CCU 余额'), 'paid series legend should render');
});

test('colab history chart keeps x-axis ticks inside the <svg>', () => {
  const html = render(makeSandbox(), paidSeries);
  const svgClose = html.indexOf('</svg>');
  const firstTick = html.indexOf('<text ');
  assert.ok(firstTick >= 0, 'chart should emit axis text');
  assert.ok(firstTick < svgClose, 'x/y tick <text> must be inside the <svg>');
});

test('colab history chart never appends to innerHTML after opening the svg', () => {
  const body = extractFunction('renderColabHistoryChart');
  assert.ok(
    !/\.innerHTML\s*\+=/.test(body),
    'renderColabHistoryChart must assign innerHTML once, not append after <svg>'
  );
});

test('colab history chart smooths a dense step-shaped history', () => {
  // 800 hourly snapshots in two flat plateaus — far more points than the chart
  // should draw. Resampling + averaging must collapse this to a bounded, smooth
  // path instead of emitting ~800 near-vertical segments.
  const many = [];
  const base = Date.parse('2026-09-10T00:00:00Z');
  for (let i = 0; i < 800; i++) {
    many.push({
      ts: new Date(base + i * 3600 * 1000).toISOString(),
      paid_balance: i < 400 ? 50 : 150,
      free_remaining: 0,
      has_paid: true,
      has_free: false,
    });
  }
  const html = render(makeSandbox(), many, 30);
  const d = (html.match(/<path d="([^"]*)"/) || [])[1] || '';
  const segments = (d.match(/[LC] /g) || []).length;
  assert.ok(segments > 0, 'should still draw a path');
  assert.ok(segments <= 200, 'dense history must be resampled/smoothed, got ' + segments + ' segments');
  assert.ok(html.includes('付费 CCU 余额'), 'paid series legend should render');
});

test('template.html is printf-safe (literal % must be %%; only %d verbs allowed)', () => {
  // template.html is fed through Go's fmt like a printf format string: every
  // literal percent sign MUST be written as %%, and the only intended verbs are
  // %d (retention days, refresh interval). A stray % (e.g. `win%2`) becomes
  // something like `%!?(int= 0)` in the served JS — a syntax error that blanks
  // the whole dashboard. This guard scans the SOURCE for such strays.
  const raw = fs.readFileSync(path.join(__dirname, 'template.html'), 'utf8');
  for (let i = 0; i < raw.length; i++) {
    if (raw[i] !== '%') continue;
    const next = raw[i + 1];
    if (next === '%' || next === 'd') {
      i++; // consume the pair
      continue;
    }
    assert.fail(
      'stray % at offset ' + i + ' (would be mangled by fmt): ...' +
        raw.slice(Math.max(0, i - 60), i + 60).replace(/\n/g, '\\n') +
        '...'
    );
  }
});

test('colab history chart asks for data instead of drawing below two points', () => {
  const one = render(makeSandbox(), paidSeries.slice(0, 1));
  assert.ok(!one.includes('<svg'), 'a single snapshot should not draw an empty chart');
  assert.ok(one.includes('暂无可用的历史快照'), 'should explain that history accumulates');
});
