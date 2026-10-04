# Changelog

## v0.11.9 (2026-10-04)

### Fixes
- **移除无效的「内存环形缓冲区」指标**: 该环形缓冲区只写不读（所有看板数据都走 SQLite），却常驻内存最多 1 万条事件（含 `failure_body`），启动时还要为此多查一次数据库；且作为定长环形结构，事件量一超过容量就永远显示 100%，健康页据此误报「负载偏高」。现已整体移除，健康页改为展示**真实**的异步落盘队列背压（`write_queue_used / write_queue_size`），并仅在队列 ≥80% 或存在丢弃时告警。
- **配置项 `max_in_memory_events` 移除**: 随环形缓冲区一并废弃；旧配置中的该键会被安全忽略，无需改动现有 YAML。
- **`refresh_seconds` 生效条件修复**: 原先该配置只在 `max_in_memory_events > 0` 时才会被合并生效（错误耦合），现改为独立生效。

## v0.11.8 (2026-10-04)

### Fixes
- **时间序列图表与所选时间范围不符**: 此前概览页主图与 KPI 迷你趋势图只聚合 `/events?limit=500` 返回的最近 500 条事件（30 天范围下仅覆盖约 11 小时），导致坐标轴跨度为 30 天、但数据只集中在最后几个桶（表现为长时间平线 + 末端突刺）。新增服务端聚合接口 `GET /api/timeseries?range=&buckets=`，按整个所选范围分桶统计请求数/Token/真实定价成本，前端主图与 4 个 KPI Sparkline 改用该接口（无数据时回退到客户端分桶）。
- **缓存命中率 Sparkline 造假**: 原先该迷你图用 `min(100, tokens/1000)` 作为曲线值，与缓存命中率无关；改为按桶的真实 `cached_tokens / input_tokens` 计算（并钳制到 100%）。

## v0.11.7 (2026-10-04)

### Features
- **折线图升级为丝滑曲线**: 新增 `smoothPath` 辅助函数，采用单调三次插值（Fritsch–Carlson）将折线路径改为平滑贝塞尔曲线，应用于时间序列主图、概览页 KPI 迷你面积图（Sparkline）与 Colab 历史配额图。相比 Catmull-Rom，单调插值不会超出数据范围，因此平滑后的面积图不会下探到基线以下或越过峰值。

## v0.11.6 (2026-10-04)

### Fixes
- **缓存命中率超过 100%**: 修复 `cacheHitRate` 未做上限约束的问题。Claude 等将缓存读取量（`cache_read_input_tokens`）与普通输入分开上报的提供方会出现 `cached > input`，此前会算出 200%+ 的命中率。现统一钳制到 `[0, 100]`，概览 KPI、事件列表徽章与详情抽屉均同步钳制。
- **时间序列图表渲染错乱**: 修复缓存率 >100% 导致 `buildTimeBuckets` 中合成成本 `(input-cached)*0.5 + ...` 变为负值，进而使折线/面积整体跌到 `$0.00` 基线以下、与 X 轴时间标签重叠的问题（表现为底部色块与异常竖线）。现对缓存率与成本取非负钳制，并在 `renderMainTimeline` 中对数值与 `maxVal` 做防御性约束。

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
