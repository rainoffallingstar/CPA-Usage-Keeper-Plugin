# Quota Monitoring Providers — Calculation Methods & Pitfalls

Last updated: 2026-10-19 (v0.11.30)

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

## 4. Ollama Cloud — Session / Weekly Quota Monitoring

### API

Ollama Cloud does **not** expose a public quota API. Instead we fetch the
`https://ollama.com/settings` page with the user's session cookie and parse the
"Cloud usage" block out of the returned HTML.

```
GET https://ollama.com/settings
Cookie: aid=...; __Secure-session=...
User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 ...
```

Reference implementation: https://github.com/jacklee-code/ollama-cloud-quota-monitor

### HTML Structure Parsed

The settings page embeds a "Cloud usage" block containing:

| Element | Extracted Field | Notes |
|---------|-----------------|-------|
| `rounded-full ... capitalize ...` span | `plan` | Plan name (e.g. "Free", "Pro") |
| `data-usage-track aria-label="...% used"` | `used` (Session / Weekly) | Two tracks: Session + Weekly |
| `data-usage-segment` buttons | `models[]` | Per-model `data-model`, `data-requests`, `width:%` |
| `flex justify-between mb-2` header spans | `status_text` | Period status text |
| `local-time data-time="..."` | `reset_at` / `reset_in_sec` | Window reset timestamp |

### Field Mapping

| HTML Field | quotaWindow Field | Notes |
|-----------|-------------------|-------|
| `aria-label` "% used" | `used` | Parsed via `(\d+(?:\.\d+)?)\s*%\s*used` |
| `100 - used` | `remaining` | Percentage-based |
| `100` | `total` | Always 100 (percentage) |
| `%` | `unit` | Percentage |
| `data-time` (ISO 8601) | `reset_at` / `reset_in_sec` | `reset_in_sec = reset_at - now` |
| `data-model` / `data-requests` / `width:%` | `models[]` | Per-model request counts + share % |

### Cookie Normalization

```go
func buildOllamaCookieHeader(sessionCookie string) string {
    // strips "Cookie:" prefix, wraps bare value as __Secure-session=...
    // accepts "aid=...; __Secure-session=..." or a bare session value
}
```

### Pitfalls

1. **No public API** — relies on scraping the settings page; page structure changes
   can break parsing (guarded by "Cloud usage" block detection).
2. **Cookie-based auth** — `aid` + `__Secure-session` cookies; expiry or missing
   cookies yield a "sign in" page (detected and reported as "未登录或 cookie 无效").
3. **Two windows only** — Session and Weekly (no monthly window). Each window can
   be independently hidden via `show_session` / `show_weekly` config flags.

---

## 5. Frontend Progress Bar Rendering (Shared)

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

## 6. API Route Registration for Quota

All four providers are registered as resource API routes:

| Provider | Resource Path |
|----------|--------------|
| OpenCode | `/v0/resource/plugins/usage-keeper/api/opencode-quota` |
| GLM | `/v0/resource/plugins/usage-keeper/api/glmcoding-quota` |
| DeepSeek | `/v0/resource/plugins/usage-keeper/api/deepseek-quota` |
| Ollama | `/v0/resource/plugins/usage-keeper/api/ollama-quota` |

---

## 7. DB Persistence

| Provider | Table | Columns |
|----------|-------|---------|
| OpenCode | `opencode_quota_accounts` | `name`, `auth_cookie`, `workspace_id` |
| GLM | `glm_coding_accounts` | `name`, `api_key`, `base_url` |
| DeepSeek | `deepseek_accounts` | `name`, `api_key` |
| Ollama | `ollama_accounts` | `name`, `session_cookie`, `show_session`, `show_weekly` |

All accounts survive plugin restarts via SQLite `ON CONFLICT ... DO UPDATE`.

---

## 8. Google Colab — Subscription Tier & CCU Quota Monitoring

Auth flow mirrors [googlecolab/colab-vscode](https://github.com/googlecolab/colab-vscode):

### Login (OAuth2 Authorization Code + PKCE, loopback callback)

1. Dashboard calls `GET /api/colab-quota?action=login&account=<name>`
2. Plugin starts an ephemeral HTTP server on `127.0.0.1:<random-port>`, generates
   a PKCE `code_verifier` + S256 `code_challenge` and a `nonce`, then returns
   the Google authorization URL with `redirect_uri=http://127.0.0.1:<port>`.
3. User authorizes in the browser → Google redirects to the loopback server
   with `?code=...&state=nonce=<id>` → the plugin captures the code.
4. Dashboard polls `GET /api/colab-quota?action=loginstate&login_id=<id>` until
   the plugin has exchanged the code for tokens via
   `POST https://oauth2.googleapis.com/token`
   (`client_id` + `client_secret` + `code` + `code_verifier` + `redirect_uri`).
5. `refresh_token` is stored obfuscated (XOR-salted, base64) in SQLite.

### Credential storage

| Table | Columns |
|-------|---------|
| `colab_quota_accounts` | `name`, `refresh_token` (obfuscated), `email` |

Access tokens are JIT-refreshed from the refresh token 2 minutes before expiry.

### Quota query

```
GET https://colab.pa.googleapis.com/v1/user-info?get_ccu_consumption_info=true
Authorization: Bearer <access_token>
```

| API Field | Display |
|-----------|---------|
| `subscriptionTier` (`NONE`/`PRO`/`PRO_PLUS`) | `Plan` (Colab 免费版 / Colab Pro / Colab Pro+) |
| `paidComputeUnitsBalance` | 付费 CCU 算力窗口（基于阶梯天花板契约推导 total 与 used） |
| `consumptionRateHourly` / `assignmentsCount` | 消耗率 CCU/h + 运行实例数 |
| `freeCcuQuotaInfo.remainingTokens` (mCCUs, Int64 string) | 免费 CCU 剩余窗口 |
| `freeCcuQuotaInfo.nextRefillTimestampSec` | 免费额度重置倒计时 |

---

### Quota Ceiling Contract (智能阶梯动态天花板契约)

#### 1. 业务事实与 API 现实约束
- **Colab Pro**: 每月发放 100 CCU，有效期 90 天（3 个月内可跨月累积，纯月费最高持有 300 CCU）。
- **Colab Pro+**: 每月发放 600 CCU，有效期 90 天（纯月费最高持有 1800 CCU）。
- **Pay-As-You-Go (按需单次购买)**: 仅允许购买 **100 CCU** 或 **500 CCU** 两种固定规格包。
- **企业用户 (Colab Enterprise)**: 无固定额度，按 GCP 组织项目统一按量结算。
- **核心约束**: Google 官方 API `v1/user-info` **仅返回当前可用余额单一数值** `paidComputeUnitsBalance`（如 `63.16`），**不返回**历史充值批次、月结重置日及过期时间。

#### 2. 契约算法（Adaptive Step-up Ceiling）
由于 100 CCU（Pro 月费/小包）、500 CCU（大包）、600 CCU（Pro+ 月费）的公约基数均为 100 CCU，采用**基线覆盖 + 100 步长阶梯阶跃算法**：

```go
func computeColabCeiling(tier string, balance float64) (total float64, used float64, hint string)
```

1. **Colab Pro (`tier == "PRO"`)**:
   - `balance <= 100.0`: 标准月度基线。`total = 100.0`, `used = max(0, 100.0 - balance)`。
   - `balance > 100.0`: 跨月累积或叠加增购。`total = ceil(balance / 100.0) * 100.0`, `used = total - balance`。
   - `balance == 0.0`: 配额耗尽。`total = 100.0`, `used = 100.0`（触发 100% 红色告警）。
2. **Colab Pro+ (`tier == "PRO_PLUS"`)**:
   - `balance <= 600.0`: 标准月度基线。`total = 600.0`, `used = max(0, 600.0 - balance)`。
   - `balance > 600.0`: 跨月累积或叠加增购。`total = ceil(balance / 100.0) * 100.0`, `used = total - balance`。
   - `balance == 0.0`: 配额耗尽。`total = 600.0`, `used = 600.0`（触发 100% 红色告警）。
3. **企业用户 (`tier == "ENTERPRISE"`)**:
   - `total = balance`, `used = 0`, 状态文案注明「企业版无固定上限 · 按量结算」，不渲染进度条比例。
4. **免费版 / 纯增购买家 (`tier == "NONE"`)**:
   - 若 `balance > 0`: `total = ceil(balance / 100.0) * 100.0`, `used = total - balance`。
   - 若 `balance == 0`: 仅展示免费 mCCUs 窗口，不展示付费条。

#### 3. 前端展示契约
- **进度条百分比**: `pct = min(100, round((used / total) * 100))`
- **数值行**: `used.toFixed(1) + " / " + total.toFixed(0) + " CCU (余 " + balance.toFixed(1) + ")"`
  - *例*: `36.8 / 100 CCU (余 63.2)`
- **颜色阈值**: `< 70%` 正常绿，`70% ~ 90%` 告警橙，`>= 90%` 告急红。
- **副标题状态行**: 附带 `status_text` 实时展示每小时消耗率与批次上限。

---

### Route & registration

| Provider | Resource Path |
|----------|--------------|
| Colab | `/v0/resource/plugins/usage-keeper/api/colab-quota` |

### Pitfalls

1. Colab requires the `https://www.googleapis.com/auth/colaboratory` scope in
   addition to `profile`/`email`.
2. `paidComputeUnitsBalance` is absent when the user has no paid balance; free
   quota fields are only present in that case.
3. `remainingTokens` is serialized as a string (ProtoJSON Int64) — parse as
   integer and treat value as milli-CCUs.
4. The OAuth client is the public Colab VS Code extension client;
   it is not private — treat refresh tokens as sensitive.


---

## 9. NewAPI-compatible panels — Wallet + Subscriptions

### Scope: stock new-api only

Verified against `QuantumNous/new-api` `main` (HEAD `058c823`, 2026-10-18) and
release `v1.0.0-rc.45`. This provider deliberately uses **only routes that
upstream ships**:

| Endpoint | Auth | Purpose |
|----------|------|---------|
| `GET /api/status` | public | `quota_per_unit`, `quota_display_type`, `usd_exchange_rate` |
| `GET /api/user/self` | `profile:read` | `quota`, `used_quota`, `request_count`, `group` |
| `GET /api/subscription/self` | `wallet:read` | active subscriptions (`amount_total` / `amount_used` / `end_time`) |
| `GET /api/subscription/plans` | `wallet:read` | `plan_id` → `title` |
| `GET /api/log/self/stat` | `usage:read` | quota consumed in `start_timestamp`…`end_timestamp` |

Not used (dddai extensions, absent upstream — see §9.1 below):

- `GET /api/billing/summary`
- `GET /api/user/self/pricing`
- `GET /api/user/messages`

### Credential and base URL

- Credential: panel **access token** (`nap_...`, `model.AccessTokenPrefix`),
  sent as `Authorization: Bearer <token>`. Model keys (`sk-...`) are rejected by
  `/api/*`, and `X-Api-Key` is rejected too.
- Base URL is per-account and editable in the dashboard (`面板地址`). Bare hosts,
  trailing slashes, and accidental `/api` / `/v1` suffixes are normalized.
- Tokens are obfuscated at rest in `newapi_accounts.token`.

### Multiple simultaneous subscriptions

A NewAPI user can hold several active subscriptions at once, and each one is
scoped to its own group (`upgrade_group`, e.g. `sub-openai` vs
`sub-claude-kiro`). Their quota pools are **not** interchangeable, so:

- Each active subscription renders as its **own progress bar**, labelled with
  its plan title (`订阅 #id` when the title cannot be resolved).
- Duplicate plan titles are disambiguated with the group name, then the
  subscription id.
- Subscriptions are ordered by soonest `end_time`.
- Expired/cancelled subscriptions are ignored.
- `amount_total == 0` means unlimited (upstream semantics) and renders as text,
  not a `$0 / $0` bar.
- The burn-rate window reports a **pooled** remaining figure, but appends
  `各订阅额度不互通` whenever more than one subscription is present; when any
  subscription is unlimited no day estimate is claimed.

### Field mapping

| Source | Card | Conversion |
|--------|------|------------|
| `quota_per_unit` | — | divide all quota values → USD |
| `amount_used` (per subscription) | window `used` | ÷ unit |
| `amount_total` | window `total` | ÷ unit |
| `amount_total - amount_used` | window `remaining` | ÷ unit |
| `end_time - now` | `status_text` 「N 天后到期」 | days |
| `next_reset_time` | `status_text` 「N 天后重置」 | days |
| `user.quota` | wallet `remaining` | ÷ unit |
| `user.used_quota` | wallet `status_text` | ÷ unit |
| 7-day `log/self/stat` quota ÷ 7 | 日均 | ÷ unit |
| wallet + finite subscriptions | 合计剩余 / 预计天数 | ÷ unit |
| `plan_title` (from `/plans`) | window label + account plan | — |
| `quota_display_type` + `usd_exchange_rate` | `面板显示 ¥…` | CNY = quota/unit×rate |

### Pitfalls

1. **`/api/billing/summary` is not portable** — it is dddai-only. Using the
   stock routes keeps the provider working on every NewAPI deployment.
2. **Unit conversion is mandatory.** Without `quota_per_unit` the card shows
   `475000000 / 950` style numbers.
3. **Wallet `total` is lifetime cumulative**, not a budget; the wallet is
   rendered as a text balance and `used` moves into `status_text`.
4. **`UserSubscription` has no plan title.** Upstream returns `plan_id` only, so
   titles need `/api/subscription/plans`.
5. **Do not sum subscription quotas into one bar.** Groups are isolated; only
   the estimate hint may report a pooled figure, with a caveat.
6. **Scope mapping** — `profile:read` (wallet), `wallet:read` (subscriptions),
   `usage:read` (logs). A missing scope degrades to a warning instead of
   blanking the whole card, and the panel's message (`ACCESS_TOKEN_SCOPE_DENIED`)
   is shown verbatim.
7. **Legacy tokens are retired** — HTTP 401 `ACCESS_TOKEN_LEGACY_RETIRED`.
8. **Log range cap** — `/api/log/self/stat` enforces `log_setting.max_range_days`
   (default 365); the 7-day query is far inside it.

### 9.1 dddai extensions (for reference, not relied upon)

| Endpoint | Content |
|----------|---------|
| `/api/billing/summary` | wallet + subscription + 7-day estimate in one call |
| `/api/user/self/pricing` | pricing tier (`pricing_tier`, factor) |
| `/api/user/messages` | in-site messages |

They were useful for validating the numbers during development, but the
provider no longer calls them.

### Route & registration

| Provider | Resource Path |
|----------|--------------|
| NewAPI | `/v0/resource/plugins/usage-keeper/api/newapi-quota` |

Actions (GET query or POST JSON): `addaccount`, `removeaccount`,
`settoken` (alias `setkey`), `refresh`. `settoken` also accepts a new
`base_url`, which backs the dashboard's 「编辑凭据」 action.

### One-click quota reset (single key)

```
GET /api/newapi-quota?action=resetkey&account=<acct>&key_id=<id>
X-NewAPI-Token: <panel access token>     # or use the stored account token
X-NewAPI-Quota-USD: <new remaining amount>
```

Upstream has **no dedicated reset endpoint**. The only way is `PUT /api/token/`,
whose handler copies request fields onto the stored row **unconditionally**
(`model.Token.Update()` selects `name, status, expired_time, remain_quota,
unlimited_quota, model_limits_enabled, model_limits, allow_ips, group,
cross_group_retry, auto_groups`). Anything omitted from the body is written as a
zero value — an early probe with `{"id":9876}` blanked `name`, `model_limits`,
`group` and `expired_time` on a live key (restored immediately afterwards).

Therefore the plugin never sends a partial body. `resetNewAPIKeyQuota`:

1. `GET /api/token/:id` — read the full current state;
2. reject `unlimited_quota` keys (no meaningful balance to reset);
3. `PUT /api/token/` — change **only** `remain_quota`, echoing every other
   assignable field verbatim (an empty `allow_ips` is sent as `""`, not `null`);
4. if the key was exhausted (`status == 4`), `PUT /api/token/?status_only=1`
   with `status: 1` — upstream refuses to enable a token whose stored balance is
   still `<= 0`, so this must come **after** the balance write;
5. re-read and verify: the stored balance matches what was requested **and**
   name/group/model limits/expiry are unchanged; a mismatch is reported instead
   of silently accepted.

`used_quota` is read-only (not in the update column list), so the lifetime usage
counter is untouched by a reset.

Requires `api_key:write`. The amount is expressed in USD and converted with
`quota_per_unit`, so the UI never handles internal units.

### DB persistence

| Provider | Table | Columns |
|----------|-------|---------|
| NewAPI | `newapi_accounts` | `name`, `base_url`, `token` (obfuscated) |
