// Pure helper functions - usable in tests without DOM.
const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
const num = (value) => Number.isFinite(Number(value)) ? Number(value) : 0;
function compact(value) { const n = num(value), abs = Math.abs(n); const trim = (v) => v.toFixed(1).replace(/\.0$/, ''); if (abs >= 1e6) return trim(n / 1e6) + 'M'; if (abs >= 1e3) return trim(n / 1e3) + 'k'; return fmt.format(n) }
const pct = (value) => Number.isFinite(value) ? value.toFixed(1) + '%' : '-';
const trimFixed = (value, digits) => Number(value).toFixed(digits).replace(/\.0+$|(\.\d*?[1-9])0+$/, '$1');
const formatMs = (value) => {
  if (!(Number.isFinite(value) && value > 0)) return '-';
  if (value < 1000) return trimFixed(value, 2) + 'ms';
  const seconds = Math.floor(value / 1000);
  const milliseconds = value - seconds * 1000;
  if (milliseconds < 0.005) return seconds + 's';
  return seconds + 's' + trimFixed(milliseconds, 2) + 'ms';
};
function totalTokens(detail) { const t = detail.tokens || {}; return num(t.total_tokens) || num(t.input_tokens) + num(t.output_tokens) }
function cacheHitRate(cachedTokens, inputTokens) {
  const i = num(inputTokens);
  if (i <= 0) return 0;
  return Math.min(100, Math.max(0, (num(cachedTokens) / i) * 100));
}
function avg(values) { const xs = values.map(num).filter((v) => v > 0); return xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : 0 }
function parseRange(rangeStr) {
  switch (String(rangeStr || '').toLowerCase().trim()) {
    case '1h': return 1;
    case '6h': return 6;
    case '24h': case 'day': return 24;
    case '7d': case 'week': return 7 * 24;
    case '30d': case 'month': return 30 * 24;
    default: return 24;
  }
}
function rangeLabel(hours) {
  switch (hours) {
    case 1: return 'Last 1 hour';
    case 6: return 'Last 6 hours';
    case 24: return 'Last 24 hours';
    case 168: return 'Last 7 days';
    case 720: return 'Last 30 days';
    default: return 'Last ' + hours + ' hours';
  }
}
function formatTokens(value) { const n = num(value); return n.toLocaleString('en-US') }
function formatRequests(value) { const n = num(value); return n.toLocaleString('en-US') }
function formatCacheHitRate(value) { return Number.isFinite(value) ? value.toFixed(1) + '%' : '0.0%' }
function timestampMs(value) { const ms = Date.parse(value); return Number.isFinite(ms) ? ms : 0 }
function eventRowClass(failed) { return failed ? 'event-failed' : '' }
function eventStatus(failed) { return failed ? 'Failed' : 'Success' }

if (typeof module !== 'undefined' && module.exports) {
  module.exports = { esc, num, compact, pct, trimFixed, formatMs, totalTokens, cacheHitRate, avg, parseRange, rangeLabel, formatTokens, formatRequests, formatCacheHitRate, timestampMs, eventRowClass, eventStatus };
}
