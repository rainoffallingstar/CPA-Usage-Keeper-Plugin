// Executes the real renderTopModels() from dashboard/template.html against a
// tiny DOM stub. Static string checks cannot catch a runtime throw inside the
// render function - which is exactly how the Top 5 card silently went blank.
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

function renderTopModels(models) {
  const container = {
    innerHTML: '',
    clientWidth: 560,
    querySelectorAll: function () {
      return [];
    },
  };
  const sandbox = {
    Math,
    Number,
    isFinite,
    console,
    document: { getElementById: (id) => (id === 'top-models-bars' ? container : null) },
    lastModelsData: models,
    esc: (s) => String(s == null ? '' : s),
    formatCost: (c) => '$' + (Number(c) || 0).toFixed(2),
    formatNum: (n) => String(Number(n) || 0),
    formatTokens: (n) => String(Number(n) || 0),
    cleanProviderName: (p) => String(p == null ? '' : p),
    showTooltip() {},
    hideTooltip() {},
  };
  vm.createContext(sandbox);
  vm.runInContext(
    [extractFunction('smoothPath'), extractFunction('renderTopModels')].join('\n'),
    sandbox
  );
  sandbox.renderTopModels();
  return container.innerHTML;
}

const MODELS = [
  { model: 'gemini-3.8-flash-high', provider: 'antigravity', requests: 13100, total_tokens: 5500000000, cost: 1423.04 },
  { model: 'gpt-5.6-terra', provider: 'codex', requests: 5300, total_tokens: 479600000, cost: 162.5 },
  { model: 'gpt-6-astra', provider: 'codex', requests: 357, total_tokens: 52300000, cost: 129.31 },
  { model: 'gpt-5.6-sol', provider: 'codex', requests: 2600, total_tokens: 262700000, cost: 92.99 },
  { model: 'deepseek-v4.1-flash', provider: 'anthropic', requests: 9500, total_tokens: 3700000000, cost: 27.42 },
  { model: 'long-tail-model', provider: 'other', requests: 10, total_tokens: 1000, cost: 5.0 },
];

test('renderTopModels executes and emits a Pareto curve', () => {
  const html = renderTopModels(MODELS);
  assert.ok(html.includes('<svg'), 'should render an svg: ' + html.slice(0, 120));
  assert.ok(html.includes('top5-pareto-grad'), 'should use the pareto gradient');
  assert.ok(html.includes('<path d="'), 'should draw the curve path');
  assert.ok(html.includes('Top 5 合计占总开销'), 'should include the concentration caption');
  assert.ok(!/NaN|undefined/.test(html), 'must not emit NaN/undefined: ' + html.slice(0, 200));
});

test('renderTopModels cumulative share is relative to total spend', () => {
  const html = renderTopModels(MODELS);
  // total = 1840.26; top1 share = 1423.04 / 1840.26 = 77.3%, top5 = 99.7%
  assert.ok(html.includes('99.7%'), 'expected top-5 cumulative ~99.7%: ' + html);
  assert.ok(html.includes('77.3%'), 'expected top-1 share ~77.3%: ' + html);
});

test('renderTopModels tolerates a single model', () => {
  const html = renderTopModels([{ model: 'solo', provider: 'p', requests: 1, total_tokens: 1, cost: 2 }]);
  assert.ok(html.includes('<svg'), 'single model should still render: ' + html.slice(0, 120));
  assert.ok(!/NaN|undefined/.test(html));
});

test('renderTopModels shows the empty state for no data', () => {
  assert.ok(renderTopModels([]).includes('暂无模型开销排行'));
  assert.ok(renderTopModels([{ model: 'z', provider: '', requests: 0, total_tokens: 0, cost: 0 }]).includes('暂无模型开销排行'));
});
