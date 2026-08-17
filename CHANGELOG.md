# Changelog

## v0.10.18 (2026-08-17)

### Features
- **Ollama Cloud 用量监控**: 新增 Ollama Cloud 配额查询，抓取 `https://ollama.com/settings` 页面解析 Session / Weekly 用量百分比、套餐名（Plan）及按模型拆分的请求数
- **Dashboard**: Quota 标签页新增 Ollama Cloud 账号管理（添加/删除/设置 Cookie/刷新）
- **配置**: 新增 `ollama_accounts` 配置项（`name` / `session_cookie` / `show_session` / `show_weekly`）
- **API**: 新增 `GET/POST /api/ollama-quota` 资源端点与 `GET/POST /usage-keeper/ollama-quota` 管理端点

实现参考 [ollama-cloud-quota-monitor](https://github.com/jacklee-code/ollama-cloud-quota-monitor)。

## v0.10.12 (2026-07-22)

### Features
- **Dashboard**: Manual refresh buttons on "By Model" and "All Events" capsule tabs
- **Dashboard**: Fuzzy model name aggregation — similar models (e.g. `gpt-4o-2024-05-13`, `gpt-4o-2024-08-06`) are grouped by normalized name with summed tokens/requests/cost
- **Dashboard**: Detailed / Aggregated view toggle on By Model table
- **Dashboard**: Total cost summary shown above the model table

## v0.2.0 (2026-06-30)

### Features

**Security:**
- API key privacy: keys hashed with SHA-224 before storage, display masked (`sk******56`)
- Per-process random salt for hashing (configurable via `api_key_hash_salt`)
- Sensitive header filtering and credential suffix stripping from source names

**Observability:**
- Health monitoring endpoint (`/health`) with storage metrics, write latency, cache hit rates
- ETag conditional caching on summary and events endpoints

**Data Management:**
- Model pricing system with CRUD endpoints (`GET/PUT/DELETE /prices`)
- Usage data export endpoint (`GET /export`)
- Usage data import with deduplication and limits (`POST /import`, 50MB/200k records)

**Quality:**
- Go test suite (24 tests covering config, summaries, events, cleanup, envelopes)
- JavaScript test suite (12 tests for dashboard helpers)
- GitHub Actions CI/CD pipeline (5-platform cross-compile, automatic releases)

**Distribution:**
- Plugin store registry (`registry.json`)
- Deployment and usage guide (`CPA_USAGE.md`)

### Code Organization
- Split into modular files: `main.go`, `source.go`, `health.go`, `pricing.go`
- Separate test files: `main_test.go`, `dashboard/helpers.js`, `dashboard/helpers.test.js`

## v0.1.0 (2026-06-30)

### Features
- Real-time usage ingestion via CPA UsagePlugin callback
- Persistent storage with embedded SQLite (modernc.org/sqlite)
- In-memory ring buffer for instant dashboard rendering
- Browser dashboard with summary cards, model breakdowns, and event history
- Quotio-compatible `GET /v0/management/usage` endpoint
- JSON REST APIs for dashboard consumption
- Management API endpoints with CPA management key auth
- Configurable retention cleanup (default 90 days)
- Auto-refresh support for dashboard (configurable 0-3600s interval)
- Dark/light theme toggle with localStorage persistence
- Cross-platform build support (macOS, Linux, Windows, FreeBSD)

### Technical Details
- Single-file Go plugin (~1276 lines) compiled as c-shared library
- Pure Go SQLite driver, no C dependencies for database
- YAML-based configuration integrated with CPA config.yaml
- Hot-reload support via CPA plugin system
