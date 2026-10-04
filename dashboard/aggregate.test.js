// Exercises the real aggregateModels() from dashboard/template.html, which
// depends on normalizeModelName() defined earlier in the same script.
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

const sandbox = { console };
vm.createContext(sandbox);
vm.runInContext(
  [extractFunction('normalizeModelName'), extractFunction('aggregateModels')].join('\n'),
  sandbox
);

const raw = [
  { model: 'deepseek-ai/deepseek-v4-flash', provider: 'deepseek', requests: 8, input_tokens: 10000, output_tokens: 5000, total_tokens: 15000, cached_tokens: 0, cost: 0 },
  { model: 'deepseek-v4-flash', provider: 'multiple', requests: 1260, input_tokens: 500000, output_tokens: 200000, total_tokens: 700000, cached_tokens: 0, cost: 0.9334 },
  { model: 'deepseek-v4-flash-free', provider: 'multiple', requests: 5, input_tokens: 2000, output_tokens: 1000, total_tokens: 3000, cached_tokens: 0, cost: 0 },
  { model: 'deepseek-v4-pro', provider: 'multiple', requests: 4163, input_tokens: 3000000, output_tokens: 1500000, total_tokens: 4500000, cached_tokens: 0, cost: 30.8104 },
  { model: 'gemini-3.5-flash', provider: 'multiple', requests: 1403, input_tokens: 800000, output_tokens: 400000, total_tokens: 1200000, cached_tokens: 0, cost: 244.4094 },
  { model: 'grok4.5\uff08free\uff09', provider: 'multiple', requests: 21, input_tokens: 5000, output_tokens: 2000, total_tokens: 7000, cached_tokens: 0, cost: 0 },
];

test('aggregateModels merges variants and sums their usage', () => {
  const grouped = sandbox.aggregateModels(raw);
  const byModel = {};
  grouped.forEach((g) => {
    byModel[g.model] = g;
  });

  assert.deepStrictEqual(Object.keys(byModel).sort(), [
    'deepseek-v4-flash',
    'deepseek-v4-pro',
    'gemini-3.5-flash',
    'grok-4.5',
  ]);

  const flash = byModel['deepseek-v4-flash'];
  assert.strictEqual(flash.count, 3, 'three flash variants should merge');
  assert.strictEqual(flash.requests, 8 + 1260 + 5);
  assert.strictEqual(flash.total_tokens, 15000 + 700000 + 3000);
  assert.ok(Math.abs(flash.cost - 0.9334) < 1e-9);

  // Sorted by total tokens descending.
  assert.strictEqual(grouped[0].model, 'deepseek-v4-pro');
});

test('aggregateModels tolerates empty input', () => {
  const out = sandbox.aggregateModels([]);
  assert.strictEqual(out.length, 0);
});
