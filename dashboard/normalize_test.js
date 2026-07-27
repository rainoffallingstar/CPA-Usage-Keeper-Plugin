function normalizeModelName(model) {
  if (!model) return "unknown";
  var s = model.toLowerCase().trim();
  s = s.replace(/^[a-z0-9_-]+\//, "");
  s = s.replace(/[：(\uff08]free[\uff09)]?$/, "");
  s = s.replace(/:free$/, "");
  s = s.replace(/-free$/, "");
  s = s.replace(/-low$/, "");
  s = s.replace(/-thinking$/, "");
  s = s.replace(/-\d{4}-\d{2}-\d{2}$/, "");
  s = s.replace(/-\d{8}$/, "");
  s = s.replace(/-20\d{6}$/, "");
  s = s.replace(/-preview(-\d{4}-\d{2}-\d{2})?$/, "");
  s = s.replace(/-latest$/, "");
  // Normalize by inserting brand-version hyphen for fuzzy merge: grok4.5 → grok-4.5
  s = s.replace(/^([a-z]+)(\d+(?:\.\d+)?)$/, "$1-$2");
  return s;
}

const tests = [
  // provider prefix
  ['deepseek-ai/deepseek-v4-flash', 'deepseek-v4-flash'],
  ['deepseek/deepseek-v4-flash', 'deepseek-v4-flash'],
  ['deepseek/deepseek-v4-pro', 'deepseek-v4-pro'],
  ['google/gemini-3.1-pro-preview', 'gemini-3.1-pro'],
  ['google/gemini-3.5-flash', 'gemini-3.5-flash'],
  ['moonshotai/kimi-k2.6', 'kimi-k2.6'],
  // free variants
  ['deepseek-v4-flash-free', 'deepseek-v4-flash'],
  ['deepseek-v4-flash:free', 'deepseek-v4-flash'],
  ['grok4.5\uff08free\uff09', 'grok-4.5'],
  ['grok4.5(free)', 'grok-4.5'],
  // low variant
  ['gemini-3.5-flash-low', 'gemini-3.5-flash'],
  ['gemini-3.1-pro-low', 'gemini-3.1-pro'],
  // preview
  ['gemini-3-flash-preview', 'gemini-3-flash'],
  ['gemini-3.1-pro-preview', 'gemini-3.1-pro'],
  ['google/gemini-3.1-pro-preview', 'gemini-3.1-pro'],
  // date suffix
  ['gpt-4o-2024-05-13', 'gpt-4o'],
  ['gpt-4o-2024-08-06', 'gpt-4o'],
  // should pass through
  ['deepseek-v4-flash', 'deepseek-v4-flash'],
  ['deepseek-v4-pro', 'deepseek-v4-pro'],
  ['gemini-3.5-flash', 'gemini-3.5-flash'],
  ['gemini-3.5-flash-thinking', 'gemini-3.5-flash'],
  ['gpt-5.6-sol', 'gpt-5.6-sol'],
  ['glm-5.2', 'glm-5.2'],
  // real data from the API
  ['deepseek-ai/deepseek-v4-flash', 'deepseek-v4-flash'],
  ['deepseek-v4-flash-free', 'deepseek-v4-flash'],
  ['deepseek-v4-flash:free', 'deepseek-v4-flash'],
  ['deepseek/deepseek-v4-flash', 'deepseek-v4-flash'],
  ['deepseek/deepseek-v4-pro', 'deepseek-v4-pro'],
  ['gemini-3.1-pro-low', 'gemini-3.1-pro'],
  ['google/gemini-3.1-pro-preview', 'gemini-3.1-pro'],
  ['google/gemini-3.5-flash', 'gemini-3.5-flash'],
  ['moonshotai/kimi-k2.6', 'kimi-k2.6'],
];

let failures = 0;
tests.forEach(([input, expected]) => {
  const result = normalizeModelName(input);
  if (result !== expected) {
    console.log('FAIL: normalizeModelName("' + input + '") = "' + result + '", expected "' + expected + '"');
    failures++;
  }
});
if (failures === 0) {
  console.log('All ' + tests.length + ' tests passed!');
} else {
  console.log(failures + '/' + tests.length + ' tests FAILED');
  process.exit(1);
}
