# Changelog

## v0.10.23 (2026-08-31)

### Fixes
- **定价表图标尺寸修复**: 修复定价表分类折叠栏中的数据库图标（Database SVG）与操作按钮图标（Delete SVG）因缺少显式尺寸约束而异常放大的问题，全局增加严格的 14px/12px/11px SVG 尺寸规范。

## v0.10.22 (2026-08-31)

### Fixes
- **OpenCode 订阅状态解析**: 针对有效账号但未开通 OpenCode Go 订阅（Free Tier/无滚动配额）的情况，优雅显示「未开通 Go 订阅」提示，避免报解析错误 `could not parse quota data from dashboard HTML`。
- **Ollama 模型调用次数归属**: 修复模型调用统计展示错位问题，将 Session（5小时窗口）和 Weekly（每周）各自的模型调用次数分别精准嵌套在对应的配额窗口进度条下方，不再全部堆叠在周统计区。

## v0.10.21 (2026-08-31)

### Fixes & UI Enhancements
- **订阅配额页 (Quota 1:1 对齐设计稿)**:
  - 顶部按 Provider 单独切换展示对应账号网格（OpenCode Go / GLM Coding / DeepSeek / Ollama Cloud），移除冗余的全部平铺大块。
  - 新增右上角「+ 添加账号」折叠输入卡片与「同步配额」按钮。
  - 配额卡片严格采用设计稿的布局与圆角边框、清晰的额度重置倒计时与模型使用明细。
- **Provider 成本分布环形图优化**:
  - 优化复合/长提供商名称清洗逻辑，避免过长文本导致图例换行混乱与高度溢出。
  - 采用 Top 5 +「其他渠道」智能聚合策略，保证图例项紧凑且高度自适应（对齐 260px 主图）。
  - 引入动态 Apple 阶梯彩色色板（蓝/紫/绿/橙/粉/青/灰），彻底解决因未命中固定 key 导致全图变灰的问题。
- **时间序列趋势自适应分桶**: 针对短时间密集调用的事件流自适应 hourly/2-hourly 分桶，展现细腻真实曲线。

## v0.10.20 (2026-08-31)

### Features
- **Apple 视觉系统重构 (Apple Design System)**: 采用纯正 Apple 视觉设计语言，支持 SF Pro 字体族、单一冷峻蓝色强调色（`#0071e3`）与深浅主题自适应。
- **概览看板 (Overview 首屏)**: 新增概览面板作为默认首页，包含总花费、调用量、Token 吞吐与缓存命中等 KPI 卡片，搭载基于真实事件流的迷你 SVG Sparkline 面积折线图（移除旧版假数据）。
- **数据可视化图表体系**:
  - 时间序列支出与请求趋势主图（支持成本/请求量切换与悬停数据浮窗 Tooltip）。
  - Provider 成本占比环形图（Donut Chart）。
  - 模型开销 Top 5 横向排行榜。
  - 内存环形缓冲区仪表盘与实时运行健康洞察。
- **滑动抽屉 (Inspector Drawer)**: 升级为 Apple 风格半透明材质侧边栏，保留 1:1 惯性跟随与速度释放拖拽手势。
- **全站中文化与交互打磨**: 包含定价编辑模态对话框、四家提供商统一配额监控卡片、Toast 胶囊提示与无障碍（WAI-ARIA）键盘导航。

## v0.10.19 (2026-08-18)

### Fixes
- **模型定价匹配**: 修复以下模型无法匹配价格的问题：
  - 冒号形式模型名（`deepseek-v4-pro:0813` / `deepseek-v4-flash:0731`）现在能匹配远程价格库中的连字符形式（`deepseek-v4-pro-0813` / `deepseek-v4-flash-0731`）
  - 带 `-thinking` / `-high` / `:preview` / 日期版本等变体后缀的模型（如 `claude-opus-4-6-thinking`、`gemini-3-7-flash-high`、`deepseek-v4-pro:preview`）自动回退到基础型号价格

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
