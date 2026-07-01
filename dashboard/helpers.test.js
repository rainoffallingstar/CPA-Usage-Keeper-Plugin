// Unit tests for dashboard helpers - run with: node --test dashboard/*.test.js
const { test } = require('node:test');
const assert = require('node:assert');

global.fmt = new Intl.NumberFormat('en-US');
const helpers = require('./helpers.js');

test('esc escapes HTML', () => {
  assert.strictEqual(helpers.esc('<script>alert(1)</script>'), '&lt;script&gt;alert(1)&lt;/script&gt;');
  assert.strictEqual(helpers.esc('foo & bar'), 'foo &amp; bar');
  assert.strictEqual(helpers.esc(null), '');
  assert.strictEqual(helpers.esc(undefined), '');
});

test('num coerces values safely', () => {
  assert.strictEqual(helpers.num(42), 42);
  assert.strictEqual(helpers.num('42'), 42);
  assert.strictEqual(helpers.num('abc'), 0);
  assert.strictEqual(helpers.num(null), 0);
  assert.strictEqual(helpers.num(undefined), 0);
});

test('pct formats percentage', () => {
  assert.strictEqual(helpers.pct(95.3), '95.3%');
  assert.strictEqual(helpers.pct(100), '100.0%');
  assert.strictEqual(helpers.pct(0), '0.0%');
  assert.strictEqual(helpers.pct(NaN), '-');
});

test('formatMs formats milliseconds', () => {
  assert.strictEqual(helpers.formatMs(500), '500ms');
  assert.strictEqual(helpers.formatMs(113.25), '113.25ms');
  assert.strictEqual(helpers.formatMs(1500), '1s500ms');
  assert.strictEqual(helpers.formatMs(0), '-');
  assert.strictEqual(helpers.formatMs(-1), '-');
});

test('totalTokens computes token sum', () => {
  const detail = { tokens: { total_tokens: 100, input_tokens: 50, output_tokens: 50 } };
  assert.strictEqual(helpers.totalTokens(detail), 100);
  const detail2 = { tokens: { input_tokens: 30, output_tokens: 20 } };
  assert.strictEqual(helpers.totalTokens(detail2), 50);
  const detail3 = { tokens: { input_tokens: 10, output_tokens: 5, cached_tokens: 8 } };
  assert.strictEqual(helpers.totalTokens(detail3), 15);
});

test('cacheHitRate computes hit rate', () => {
  assert.strictEqual(helpers.cacheHitRate(50, 100), 50);
  assert.strictEqual(helpers.cacheHitRate(0, 100), 0);
  assert.strictEqual(helpers.cacheHitRate(0, 0), 0);
  assert.strictEqual(helpers.cacheHitRate(100, 0), 0);
  assert.strictEqual(helpers.cacheHitRate(200, 100), 100);
});

test('avg computes average', () => {
  assert.strictEqual(helpers.avg([1, 2, 3, 4, 5]), 3);
  assert.strictEqual(helpers.avg([0]), 0);
  assert.strictEqual(helpers.avg([]), 0);
  assert.strictEqual(helpers.avg([100, 200, 300]), 200);
});

test('parseRange parses range strings', () => {
  assert.strictEqual(helpers.parseRange('1h'), 1);
  assert.strictEqual(helpers.parseRange('6h'), 6);
  assert.strictEqual(helpers.parseRange('24h'), 24);
  assert.strictEqual(helpers.parseRange('day'), 24);
  assert.strictEqual(helpers.parseRange('7d'), 168);
  assert.strictEqual(helpers.parseRange('week'), 168);
  assert.strictEqual(helpers.parseRange('30d'), 720);
  assert.strictEqual(helpers.parseRange('month'), 720);
  assert.strictEqual(helpers.parseRange(''), 24);
  assert.strictEqual(helpers.parseRange(null), 24);
});

test('rangeLabel returns human readable labels', () => {
  assert.strictEqual(helpers.rangeLabel(1), 'Last 1 hour');
  assert.strictEqual(helpers.rangeLabel(24), 'Last 24 hours');
  assert.strictEqual(helpers.rangeLabel(168), 'Last 7 days');
  assert.strictEqual(helpers.rangeLabel(720), 'Last 30 days');
});

test('formatTokens formats with locale', () => {
  assert.strictEqual(helpers.formatTokens(1234), '1,234');
  assert.strictEqual(helpers.formatTokens(0), '0');
});

test('eventStatus returns correct status text', () => {
  assert.strictEqual(helpers.eventStatus(true), 'Failed');
  assert.strictEqual(helpers.eventStatus(false), 'Success');
});

test('eventRowClass returns class for failed events', () => {
  assert.strictEqual(helpers.eventRowClass(true), 'event-failed');
  assert.strictEqual(helpers.eventRowClass(false), '');
});
