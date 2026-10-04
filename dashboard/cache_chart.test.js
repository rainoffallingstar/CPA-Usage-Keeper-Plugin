// Regression tests for the cache-rate clamping + timeline cost math.
//
// These tests extract the real functions from dashboard/template.html (the file
// that is actually embedded and served) and run them, so the shipped code is
// covered instead of a duplicated copy.
//
// Run with: node --test dashboard/*.test.js
const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const templateSource = fs
  .readFileSync(path.join(__dirname, 'template.html'), 'utf8')
  // The template is fed through Go's fmt.Sprintf, so every literal '%' in the
  // shipped JS/CSS is written as '%%' on disk. Undo that to get valid JS.
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

const sandbox = { Date, Math, Number, isFinite, console };
vm.createContext(sandbox);
vm.runInContext(
  [extractFunction('clampPct'), extractFunction('bucketCost'), extractFunction('buildTimeBuckets')].join('\n'),
  sandbox
);

test('clampPct bounds cache hit rates to [0, 100]', () => {
  assert.strictEqual(sandbox.clampPct(42), 42);
  assert.strictEqual(sandbox.clampPct(100), 100);
  assert.strictEqual(sandbox.clampPct(150), 100);
  assert.strictEqual(sandbox.clampPct(200), 100);
  assert.strictEqual(sandbox.clampPct(-5), 0);
  assert.strictEqual(sandbox.clampPct(NaN), 0);
  assert.strictEqual(sandbox.clampPct(undefined), 0);
});

test('bucketCost never goes negative even when cached exceeds input', () => {
  // Claude-style: cache reads far exceed fresh input tokens.
  assert.ok(sandbox.bucketCost(1000, 0, 5000) >= 0);
  assert.ok(sandbox.bucketCost(0, 0, 0) >= 0);
  const normal = sandbox.bucketCost(1000000, 100000, 0);
  assert.ok(Math.abs(normal - (1e6 / 1e6) * 0.5 - (1e5 / 1e6) * 2.0) < 1e-12);
});

test('buildTimeBuckets produces non-negative costs for cache-heavy events', () => {
  const now = Date.now();
  const events = [];
  for (let i = 0; i < 6; i++) {
    events.push({
      timestamp: new Date(now - (i + 1) * 3600 * 1000).toISOString(),
      total_tokens: 5000,
      input_tokens: 2000,
      output_tokens: 0,
      // > 100% is what the backend used to emit for Claude-style records
      cache_hit_rate: 400,
    });
  }
  const buckets = sandbox.buildTimeBuckets(events, 24);
  assert.strictEqual(buckets.length, 12);
  buckets.forEach((b) => {
    assert.ok(b.cost >= 0, 'bucket cost must be >= 0, got ' + b.cost);
  });
  const total = buckets.reduce((sum, b) => sum + b.cost, 0);
  assert.ok(total > 0, 'expected some cost for cache-read dominated events');
});

test('buildTimeBuckets applies the documented cost formula', () => {
  const now = Date.now();
  const events = [
    {
      timestamp: new Date(now - 60 * 1000).toISOString(),
      total_tokens: 300000,
      input_tokens: 1000000,
      output_tokens: 100000,
      cache_hit_rate: 50,
    },
  ];
  const buckets = sandbox.buildTimeBuckets(events, 24);
  const total = buckets.reduce((sum, b) => sum + b.cost, 0);
  const cached = Math.round(1000000 * 0.5);
  const expected =
    Math.max(0, 1000000 - cached) / 1e6 * 0.5 + (100000 / 1e6) * 2.0 + cached / 1e6 * 0.1;
  assert.ok(Math.abs(total - expected) < 1e-12, 'expected ' + expected + ', got ' + total);
});
