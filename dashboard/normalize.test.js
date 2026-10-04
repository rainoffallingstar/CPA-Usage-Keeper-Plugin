// Exercises the real normalizeModelName() from dashboard/template.html.
// (The previous normalize_test.js hand-copied the function and used the
// `_test.js` suffix, so CI never ran it and it never covered shipped code.)
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

const cases = [
  ['deepseek-ai/deepseek-v4-flash', 'deepseek-v4-flash'],
  ['deepseek/deepseek-v4-pro', 'deepseek-v4-pro'],
  ['google/gemini-3.1-pro-preview', 'gemini-3.1-pro'],
  ['moonshotai/kimi-k2.6', 'kimi-k2.6'],
  ['deepseek-v4-flash-free', 'deepseek-v4-flash'],
  ['deepseek-v4-flash:free', 'deepseek-v4-flash'],
  ['grok4.5\uff08free\uff09', 'grok-4.5'],
  ['grok4.5(free)', 'grok-4.5'],
  ['gemini-3.5-flash-low', 'gemini-3.5-flash'],
  ['gemini-3.1-pro-low', 'gemini-3.1-pro'],
  ['gemini-3-flash-preview', 'gemini-3-flash'],
  ['gpt-4o-2024-05-13', 'gpt-4o'],
  ['gpt-5.6-sol', 'gpt-5.6-sol'],
  ['glm-5.2', 'glm-5.2'],
];

test('normalizeModelName collapses provider/date/variant suffixes', () => {
  cases.forEach(([input, expected]) => {
    assert.strictEqual(sandbox.normalizeModelName(input), expected, 'normalizeModelName(' + input + ')');
  });
});
