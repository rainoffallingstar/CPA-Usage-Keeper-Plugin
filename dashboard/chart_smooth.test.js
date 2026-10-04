// Tests for the smooth (monotone-cubic) SVG line path helper.
//
// smoothPath is extracted from the real dashboard/template.html so the shipped
// code is what gets exercised, and the template is also asserted to actually
// route its three line/area charts through it.
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

const sandbox = { Math, Number, isFinite };
vm.createContext(sandbox);
vm.runInContext(extractFunction('smoothPath'), sandbox);

test('smoothPath is wired into all line/area charts', () => {
  assert.ok(templateSource.includes('var linePath=smoothPath(points)'), 'sparkline should smooth');
  assert.ok(templateSource.includes('var pathStr=smoothPath(points)'), 'main timeline should smooth');
  assert.ok(templateSource.includes('return smoothPath(pts)'), 'colab history chart should smooth');
});

test('top models card renders a curve chart, not progress bars', () => {
  const start = templateSource.indexOf('function renderTopModels(');
  const end = templateSource.indexOf('\nasync function renderOverviewInsights', start);
  assert.ok(start >= 0 && end > start, 'renderTopModels should be present');
  const body = templateSource.slice(start, end);
  assert.ok(body.includes('smoothPath('), 'should draw a smooth curve');
  assert.ok(body.includes('<path d="'), 'should emit an SVG path');
  assert.ok(!body.includes('progress-bar-fill'), 'should no longer use progress bars');
  assert.ok(body.includes('top5-area-grad'), 'should fill the area under the curve');
});

test('smoothPath handles empty and single points', () => {
  assert.strictEqual(sandbox.smoothPath([]), '');
  assert.strictEqual(sandbox.smoothPath(null), '');
  const one = sandbox.smoothPath([{ x: 3, y: 4 }]);
  assert.ok(one.startsWith('M 3.0 4.0'));
  assert.ok(!one.includes('C'));
});

test('smoothPath emits cubic segments through the data points', () => {
  const pts = [{ x: 0, y: 0 }, { x: 10, y: 5 }, { x: 20, y: 3 }, { x: 30, y: 8 }];
  const d = sandbox.smoothPath(pts);
  assert.ok(d.startsWith('M 0.0 0.0'), 'starts at first point: ' + d);
  assert.ok(d.includes(' C '), 'uses cubic segments: ' + d);
  assert.ok(d.trim().endsWith('30.0 8.0'), 'ends at last point: ' + d);
  assert.ok(!/NaN|undefined|Infinity/.test(d), 'no invalid numbers: ' + d);
});

test('smoothPath never overshoots the data range', () => {
  // Deliberately non-monotone data (up/down/up) - a naive Catmull-Rom would
  // overshoot here and dip below the baseline.
  const pts = [{ x: 0, y: 10 }, { x: 10, y: 0 }, { x: 20, y: 10 }, { x: 30, y: 0 }, { x: 40, y: 10 }];
  const ys = pts.map((p) => p.y);
  const minY = Math.min(...ys);
  const maxY = Math.max(...ys);
  const d = sandbox.smoothPath(pts);
  const controlYs = [ys[0]];
  const re = /C ([\d.-]+) ([\d.-]+) ([\d.-]+) ([\d.-]+) ([\d.-]+) ([\d.-]+)/g;
  let match;
  while ((match = re.exec(d)) !== null) {
    controlYs.push(Number(match[2]), Number(match[4]), Number(match[6]));
  }
  controlYs.forEach((y) => {
    assert.ok(y >= minY - 1e-6 && y <= maxY + 1e-6, 'control y ' + y + ' outside [' + minY + ',' + maxY + ']');
  });
});

test('smoothPath degrades to a straight line for two flat points', () => {
  assert.strictEqual(sandbox.smoothPath([{ x: 0, y: 5 }, { x: 10, y: 5 }]), 'M 0.0 5.0 L 10.0 5.0');
  const flat = sandbox.smoothPath([{ x: 0, y: 7 }, { x: 10, y: 7 }, { x: 20, y: 7 }]);
  assert.ok(!/NaN/.test(flat));
});

test('smoothPath sanitises non-finite coordinates', () => {
  const d = sandbox.smoothPath([{ x: 0, y: 1 }, { x: NaN, y: 2 }]);
  assert.ok(!/NaN|undefined/.test(d), 'fallback must not emit NaN: ' + d);
});
