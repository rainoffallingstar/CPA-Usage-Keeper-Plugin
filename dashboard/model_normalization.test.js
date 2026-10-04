// The JS half of the shared golden vectors. The Go half lives in
// model_normalization_test.go and asserts the same file against
// normalizePriceModel, so the dashboard's model grouping and the backend's
// price lookup cannot drift apart.
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

const sandbox = {};
vm.createContext(sandbox);
vm.runInContext(extractFunction('normalizeModelName'), sandbox);

const fixture = JSON.parse(
  fs.readFileSync(path.join(__dirname, '..', 'testdata', 'model_normalization.json'), 'utf8')
);

test('normalizeModelName matches the shared golden vectors', () => {
  assert.ok(Array.isArray(fixture.cases) && fixture.cases.length > 0, 'fixture has cases');
  fixture.cases.forEach(({ model, base }) => {
    assert.strictEqual(
      sandbox.normalizeModelName(model),
      base,
      'normalizeModelName(' + model + ')'
    );
  });
});

test('normalizeModelName is idempotent on every fixture base', () => {
  fixture.cases.forEach(({ base }) => {
    assert.strictEqual(sandbox.normalizeModelName(base), base, 'base ' + base + ' is not stable');
  });
});
