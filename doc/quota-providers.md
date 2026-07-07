# Quota Monitoring Providers — Calculation Methods & Pitfalls

Last updated: 2026-07-07 (v0.10.9)

---

## 1. DeepSeek — Balance Monitoring

### API
```
GET https://api.deepseek.com/user/balance
Authorization: Bearer <api_key>
```

### Raw API Response Shape

```json
{
  "is_available": true,
  "balance_infos": [
    {
      "currency": "CNY",
      "total_balance": "33.12",
      "granted_balance": "0",
      "topped_up_balance": "33.12"
    }
  ]
}
```

### Field Mapping

| API Field | quotaWindow Field | Notes |
|-----------|-------------------|-------|
| `topped_up_balance` | `remaining` | The actual recharge balance (余额) |
| tier cap | `total` | `ceil(topped_up / 100) * 100` (next ¥100 boundary) |
| `tier_cap - topped_up` | `used` | Amount consumed within this tier |
| `granted_balance` | `remaining` (Granted label) | Free credits granted |

### Tiered Pricing Logic (Step-up billing ladder)

DeepSeek uses a step-up billing model: the more you top up, the higher the tier cap.
The `total` displayed is NOT the sum of all past top-ups — it's the **current tier ceiling**.

```
top-up 37 CN¥  → tier cap = 100 CN¥ (next ¥100 boundary)
top-up 145 CN¥ → tier cap = 200 CN¥ (next ¥100 boundary)
top-up 233 CN¥ → tier cap = 300 CN¥
```

Progress bar formula:
```
used = tier_cap - topped_up
pct  = used / tier_cap * 100
```

### Pitfalls Encountered

1. **API does not return monthly/rolling window limits** — only total balance. No `reset_in_sec`.
2. **`total_balance` may include granted credits already consumed** — use `topped_up_balance` for accurate remaining recharge amount.
3. **Combined grant + balance misleads** — display `Granted (Free)` separately from the tier bar.

---

## 2. GLM Coding Plan — Quota Limit Monitoring

### APIs

```
GET https://open.bigmodel.cn/api/monitor/usage/quota/limit
GET https://open.bigmodel.cn/api/monitor/usage/model-usage?startTime=X&endTime=Y
Authorization: <api_key>
```

### Raw API Response (quota/limit)

```json
{
  "data": {
    "limits": [
      {
        "type": "TOKENS_LIMIT",
        "used": 0,
        "total": 0,
        "percentage": 100,
        "currentValue": 0
      },
      {
        "type": "TIME_LIMIT",
        "used": 0,
        "total": 0,
        "percentage": 0,
        "currentValue": 0
      }
    ]
  }
}
```

### Field Mapping

| API Field | quotaWindow Field | Notes |
|-----------|-------------------|-------|
| `percentage` (API) → `100 - percentage` | `remaining` | ⚠️ **API returns REMAINING%, not used%** — must flip |
| `currentValue` | `used` | Current consumed value |
| `total` | `total` | ⚠️ **Often 0 in API** — must derive from percentage |
| `type` | determines `label` | `TOKENS_LIMIT` → "Token (5h)", `TIME_LIMIT` → "MCP (1M)" |

### Critical Pitfalls (Multiple Iterations of Fixing)

#### ❌ Pitfall 1: API returns **remaining%**, not used%
**Symptom**: Progress bar showed 100% when API said 100%.
**Root cause**: We inverted `percentage = 100 - percentage` (treating it as used%), but GLM returns remaining%.
**Fix**: Reverted to `percentage = 100 - percentage` (correct: it IS used%).

#### ❌ Pitfall 2: `used` and `total` fields are always 0 in API
**Symptom**: Dashboard always showed `0.0 / 0.0 %` regardless of actual usage.
**Root cause**: GLM's quota/limit endpoint does NOT populate the `used`/`total` JSON fields.
**Fix**: Frontend fallback — when `total === 0`, derive from `remaining%`:
```js
if (total === 0) {
    total = 100;
    var r = Math.max(0, Math.min(100, remain));
    used = 100 - r;
}
```

#### ❌ Pitfall 3: Base URL clash (LLM API path ≠ monitoring API path)
**Symptom**: `HTTP 404: /api/paas/v4/api/monitor/usage/quota/limit`
**Root cause**: LLM API base is `https://open.bigmodel.cn/api/paas/v4`, but monitoring endpoint is `https://open.bigmodel.cn/api/monitor/...`. Appending monitoring path to the LLM base produces `/api/paas/v4/api/monitor/...`.
**Fix**: Frontend default changed from `/api/paas/v4` to `https://open.bigmodel.cn`. Backend strips `/paas/` suffix from base URL.

#### ❌ Pitfall 4: Multiple toggle cycles due to wrong % direction
**Symptom**: Progress bar appeared empty (0%) when token quota was actually 100% exhausted.
**Root cause**: We tried to treat `percentage` as remaining% and compute `used = total - remaining`, but the fields were still 0.
**Fix**: Reverted to the original logic from git commit `1544574`.

### Correct Final Logic

```go
func fetchGlmQuotaLimits(url, key string) {
    // ... HTTP GET ...
    for _, l := range r.Data.Limits {
        l.Percentage = 100 - l.Percentage  // API returns used%, flip to remaining%
        // No used/total population — frontend JS handles the fallback
        limits = append(limits, l)
    }
}
```

---

## 3. OpenCode Go — Rolling Quota Monitoring

### API

OpenCode Go uses multiple internal APIs (workspace list, quota windows).
Quota windows are **rolling time-based** (5h, weekly, monthly) with explicit `reset_in_sec`.

### quotaWindow Fields Used

| Field | Meaning |
|-------|---------|
| `label` | Time window name (e.g., "5h Rolling", "Weekly", "Monthly") |
| `used` | Tokens/calls consumed in the window |
| `total` | Window limit |
| `remaining` | `total - used` (but API provides `used` and `total` directly) |
| `unit` | `%` (percentage) or token count |
| `reset_in_sec` | Seconds until the rolling window resets |

### Reset Time Display

Frontend uses `formatReset(secs)` function:
```
secs >= 86400  → "5d 15h 0m"
secs >= 3600   → "3h 22m"
secs < 3600    → "45m"
```

### Pitfalls

1. **Multiple windows per account** — each account can have 3 rolling windows (5h, weekly, monthly). Must iterate all.
2. **Cookie-based auth** — uses session cookies, not API keys. Cookie expiry causes authentication failures.
3. **Workspace isolation** — quotas are per-workspace. Must select the correct workspace before fetching.

---

## 4. Frontend Progress Bar Rendering (Shared)

### Color Thresholds

| Usage % | Bar Color | Warning |
|---------|-----------|---------|
| 0–70% | Green (`#10b981`) | None |
| 70–90% | Yellow (`#f59e0b`) | "⚠ Warning: usage above 70%" |
| 90–100% | Red (`#ef4444`) | "Quota exhausted — exceeded 90%" |

### `formatReset(secs)` Helper

```js
function formatReset(secs) {
    var d = Math.floor(secs / 86400);
    var h = Math.floor((secs % 86400) / 3600);
    var m = Math.floor((secs % 3600) / 60);
    var parts = [];
    if (d > 0) parts.push(d + 'd');
    if (h > 0 || d > 0) parts.push(h + 'h');
    parts.push(m + 'm');
    return parts.join(' ');
}
```

---

## 5. API Route Registration for Quota

All three providers are registered as resource API routes:

| Provider | Resource Path |
|----------|--------------|
| OpenCode | `/v0/resource/plugins/usage-keeper/api/opencode-quota` |
| GLM | `/v0/resource/plugins/usage-keeper/api/glmcoding-quota` |
| DeepSeek | `/v0/resource/plugins/usage-keeper/api/deepseek-quota` |

---

## 6. DB Persistence

| Provider | Table | Columns |
|----------|-------|---------|
| OpenCode | `opencode_quota_accounts` | `name`, `auth_cookie`, `workspace_id` |
| GLM | `glm_coding_accounts` | `name`, `api_key`, `base_url` |
| DeepSeek | `deepseek_accounts` | `name`, `api_key` |

All accounts survive plugin restarts via SQLite `ON CONFLICT ... DO UPDATE`.
