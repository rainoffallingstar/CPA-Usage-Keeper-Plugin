# Changelog

## v0.11.22 (2026-10-08)

### Fixes
- **Ollama Cloud 配额恢复可用（两处真实根因）**: 用户反馈「显示 cookie 无效，或者没有抓到页面」。
  - **① Cookie 被全角标点污染**：从中文环境浏览器/聊天工具复制的 Cookie 里，分隔用的 `;` 被替换成了全角 `；`（U+FF1B）。ollama.com 只按 ASCII `;` 切分 Cookie 头，于是 `aid=…；__Secure-session=…` 被当成**单个** aid Cookie 发出，`__Secure-session` 从未送达 → 服务端按未登录渲染 → 我们误报「cookie 无效」。现在新增 `normalizeOllamaCookie`：统一修正全角 `；＝，：`、全角空格、弯引号与零宽字符，并重新拼装规范的 `name=value` 片段（顺带清掉重复分隔符与尾随分号）。
    - 同时修正「裸 session 值」识别：真实的 `__Secure-session` 是 base64 且以 `==` 结尾，原先用「是否含 `=`」判断会把 ~460 字符的 base64 误当成 cookie **名**。现改为校验首段是否为合法且足够短的 RFC 6265 cookie 名。
  - **② settings 页面改版导致区块提取失效**：ollama.com 于 2026-10 改版，`Cloud usage` 由 `<span>` 变为 `<h2>`，旧的 `<span>Cloud usage</span>` 精确匹配与 `</div><script>` 终止符双双失配。现改为**标签无关**地匹配标题，并把区块结束锚定在其后紧随的「notify me」表单上。
    - 另修正套餐徽章（`pro`）解析：它已迁移到 `Cloud usage` 上方的「Usage credits」卡片中，因此必须从**整页**而非提取出的区块里解析。
    - 「Session usage / Weekly usage」标签、`data-usage-track`、`data-usage-segment`、`local-time` 重置时间在新版页面下均已验证可正常解析。

### Tests
- 新增 `testdata/ollama_settings_cloud_usage.html`：**真实抓取**的改版后 settings 页面片段（已剥离凭证与邮箱），作为回归夹具；
- 新增 `TestParseOllamaQuotaHTMLNewSettingsLayout`（新版版式端到端解析：套餐/双窗口/模型用量/重置时间）、`TestBuildOllamaCookieHeaderRepairsFullWidthPunctuation`（全角分号回归）、`TestBuildOllamaCookieHeaderCleansPairList`（6 组 Cookie 形态，含 base64 填充边界）、`TestExtractOllamaCloudUsageBlockAcceptsBothHeadingTags`（新旧标题标签与终止符）。

## v0.11.21 (2026-10-08)

### Features & UI
- **推出 5 款鲜艳明快的 Material Design 3 (M3) 亮色系主题**:
  - 应用户对更多鲜艳与亮色系、Material Design 3 风格的诉求，全面引入 Google M3 动态色彩体系（Dynamic Color / Tonal Palettes）；
  - **M3 紫罗兰 (`m3-purple` / Material Iris)**：Google 经典标志紫调（`#6750a4`），艺术优雅且富有层次；
  - **M3 蔚蓝晴空 (`m3-ocean` / Material Ocean)**：高饱和清澈加州晴海蓝（`#0284c7`），明朗通透；
  - **M3 薄荷青翠 (`m3-mint` / Material Mint)**：高生机活力薄荷青绿（`#059669`），自然呼吸与舒润护眼；
  - **M3 落日珊瑚 (`m3-sunset` / Material Sunset)**：热烈灿烂的落日暖阳与珊瑚橙红（`#f95738`），能量充沛；
  - **M3 糖果玫瑰 (`m3-rose` / Material Rose)**：元气甜美的时尚覆盆子洋红（`#e11d48`），灵动吸睛；
  - 全部 M3 主题拥有定制的彩色高对比度图表色盘（涵盖折线图、甜甜圈图、条形图与指示点）；
- **主题下拉菜单升级为分类分组（Grouped Popover）**:
  - 菜单划分为「Material 3 鲜艳系」、「经典浅色」、「暗黑与极客」、「系统自适应」四大阵营；
  - 支持精致的内部平滑滚动条与各主题专属矢量图标（花朵、波浪、绿叶、烈焰、心形等），视觉一目了然。

## v0.11.20 (2026-10-08)

### Features & UI
- **6 套全新专业视觉主题与智能系统跟随**:
  - 全新设计并实现 **浅色极简** (Apple Light)、**暗黑深邃** (Apple Dark)、**极客冰霜** (Nord Aurora)、**赛博暗夜** (Dracula Neon)、**暖阳纸墨** (Warm Sepia)、**终端翡翠** (Cyber Emerald) 与 **跟随系统** (System Auto) 模式；
  - 覆盖全站背景、表面、卡片、文字、交互控件与图表调色板（Provider 成本环形图、概览趋势图、Colab 历史快照折线图完全联动）；
  - 全局平滑过渡动效（Color Transitions），切换主题时无生硬闪烁；主题偏好持久化至 `localStorage`。
- **顶栏控件排布与自适应重构**:
  - 主题切换按钮重构为精致紧凑的图标下拉按钮（Icon-only Dropdown），解决按钮文本过长导致的顶栏折行问题；
  - 展开菜单呈现三色色卡预览胶囊、中英文名及风格描述；
  - 顶栏左右区域统一锁定 `nowrap`，增加视觉微分割线，修复窄屏下运行状态胶囊文字垂直折断的布局缺陷。

### Tests & Tooling
- **新增主题系统自动化测试** `dashboard/theme.test.js`：覆盖全部主题 CSS 变量完整性、DOM 结构与主题状态机切换；
- 优化 `scripts/migrate-plugin.sh` 端口探测逻辑，自动从配置中读取运行端口（8317/18317）进行热重载健康验证。

## v0.11.18 (2026-10-04)

### 一致性（消除同类 bug 的根源）
- **统一前后端模型名归一化口径 + 共享 golden 测试向量**: 归一化此前有 3 份实现（前端 `normalizeModelName`、后端 `modelVariants`/`stripVariantSuffix`、后端 `normalizePriceModel`），缓存率 >100% 与模型成本 $0 两个 bug 都源于口径漂移。
  - 新增 `testdata/model_normalization.json` 作为**前后端共用**的向量文件（15 组），Go 侧 `TestModelNormalizationFixture` 断言 `normalizePriceModel`，JS 侧 `dashboard/model_normalization.test.js` 断言 `normalizeModelName`，两侧必须对同一份文件得出相同结果；
  - 后端后缀集补齐 `-thinking` / `-latest` / `-preview`，与前端对齐（**日期/版本后缀故意不剥离**：前端为分组展示会合并版本，而定价必须区分版本，已在向量文件与代码注释中说明）。

### 测试
- **新增「整页初始化」冒烟测试** `dashboard/init_smoke.test.js`: 用 DOM stub + 按端点分发的 fetch stub 在 VM 中**运行真实的 dashboard `<script>`**，然后逐个渲染 6 个 tab，断言渲染期间有非空写入、包含预期数据（如模型名 / `<svg>` / 状态标题），且不含 `undefined` / `NaN` / `[object Object]`。已实测它同时能抓到两类缺陷：
  - 渲染前抛异常（即 v0.11.12「Top 5 卡片空白」的 TypeError 类型）；
  - 数据静默丢失（`esc()` 会把 undefined 渲染成空串，仅靠「无 undefined」断言抓不到，故加入正向断言）。

## v0.11.17 (2026-10-04)

### Performance
- **删除失效索引并让索引真正服务查询**: 改用 `datetime(timestamp)` 过滤后，`idx_usage_events_timestamp` / `idx_usage_events_ts_id` 已不被任何查询使用，却仍在每次写入时被维护（纯写放大）。现 `DROP` 这两个死索引与旧的 `idx_usage_events_dt`，改为复合表达式索引 `(datetime(timestamp) DESC, id DESC)`；`EXPLAIN QUERY PLAN` 确认事件查询已走 **COVERING INDEX**（`SEARCH usage_events USING COVERING INDEX idx_usage_events_dt_id`）。
- **`/api/timeseries` 由每桶一条查询合并为单条聚合 SQL**: 原来 12 个桶 = 12 条 `GROUP BY model`，现在用 `julianday` 计算桶序号，一次 `GROUP BY bucket, model` 取回全部数据；配合已有 ETag，概览刷新的查询开销从 12 条降到 1 条。

### Fixes
- **保留期清理改为定时触发**: 原先只在累计落盘 ≥1000 条时触发，低流量实例可能长期不清理、数据超过 `retention_days`。现增加每小时维护任务执行清理。
- **新增 SQLite 周期性维护**: 每小时执行 `PRAGMA optimize` 与 `PRAGMA wal_checkpoint(TRUNCATE)`，避免查询计划退化与 WAL 无限增长（此前全库没有这两条）。
- **新增内存环形错误日志（容量 20）**: 健康接口新增 `runtime.recent_errors`，记录最近的 panic / 落盘失败 / 定价同步失败 / 数据库初始化失败（含时间、错误码、消息），健康页新增「最近错误」卡片 —— 计数器只能告诉你"出错了"，它能告诉你"错在哪"。有界且真正被读取（这是 v0.11.9 删掉的那个环形缓冲该有的用途）。
- **概览页不再无条件拉取 500 条事件**: 图表已由 `/timeseries` 驱动，`/events?limit=500` 改为**仅在 timeseries 失败时**兜底加载。

### Tests
- 新增 `TestCreateTablesAndIndexes`（`createTables` 在每次启动运行，索引表达式写错会让插件直接不可用，故显式守护并打印查询计划）。
- 新增 `TestHandleTimeseriesBucketPlacement`（校验 SQL 侧 `julianday` 分桶位置正确）。
- 新增 `TestErrorLogIsBoundedAndNewestLast`（有界、淘汰最旧、返回副本）。

## v0.11.16 (2026-10-04)

### Fixes
- **热更新后浏览器仍显示旧界面**: Dashboard HTML 响应此前只带 `Content-Type`，没有任何缓存指令，浏览器会按启发式规则缓存整页 —— 换掉 dylib 后刷新仍看到旧 UI。现在返回 `Cache-Control: no-store, must-revalidate` 与 `Pragma: no-cache`，刷新必定拿到最新界面（JSON API 的 ETag 缓存不受影响）。
- **定价同步按钮文案过长**: 原先按钮写着完整域名「从 modelprice.boxtech.icu 同步」，占满标题栏。改为「**同步定价**」，数据来源移入 `title` 悬浮提示；同步完成后的短提示「已同步 N 条」保持不变。

## v0.11.15 (2026-10-04)

### Fixes
- **定价同步偶发超时（`context deadline exceeded`）**: 两处并发触发的同步会同时拉取约 700KB 的上游数据并互相抢带宽 —— 插件启动时的立即同步与手动「同步」点击/6 小时定时任务会重叠。现在用 `priceSyncRunMu.TryLock()` 保证同一时刻只有一个同步在跑；客户端超时从 30s 提到 45s，并增加一次 3s 退避重试（上游偶尔不在超时内返回响应头）。
- **「从 modelprice.boxtech.icu 同步」按钮把失败报成成功**: 同步失败时后端返回 500 + `{"error":…}`，而旧代码不检查 `resp.ok`，直接把 `data.synced` 的 `undefined` 渲染成"已同步 undefined 条"并弹出**成功**提示。现在检查响应状态、展示真实错误原因（含超时识别），失败时弹出错误 toast 并在定价面板顶部显示可重试的错误横幅；同步期间禁用按钮防重复点击，并用 `AbortController` 120s 兜底避免无限等待。
- **健康告警措辞更准确**: `price_sync_failed` 现在区分"使用 N 条缓存价格"与"尚无任何缓存价格，成本将显示为 $0"，不再笼统声称回退到最后已知价格。
- **定价搜索框加 debounce**: 与请求日志一致（300ms），避免每次按键都整表重渲染。

## v0.11.14 (2026-10-04)

### Fixes
- **6 个模型成本恒为 $0（定价与前端口径不一致）**: 前端 `normalizeModelName` 会把 `-low` / `-free` / `:free` / `（free）` 合并到基础模型，但后端定价匹配不会，导致同族模型"部分有价、部分 $0"。现补齐统一归一化：
  - 新增 `normalizePriceModel`，剥离 `（free）`/`(free)`/`:free`/`-free`/`-low`（可叠加），并补上字母-数字连字符（`grok4.5` → `grok-4-5`）；
  - 裸名缺失时回退到已发布的 `-preview` 条目（`gemini-3.1-pro` → `gemini-3-1-pro-preview`），该回退放在非递归包装层以避免与 `stripVariantSuffix` 形成死循环；
  - 实测：真实库中 44 个已用模型中，未定价数量从 **6 → 0**。

  受影响并已修复的模型：`gemini-3.5-flash-low`、`gemini-3.1-pro`、`gemini-3.1-pro-low`、`grok4.5（free）`、`deepseek-v4-flash-free`、`deepseek-v4-flash:free`。
- **内置兜底定价**: `gemini-3.1-pro` 上游只发布 preview 形态，新增 `defaultPrices` 播入一条默认价（输入 $2 / 输出 $12 / 缓存 $0.2 每 1M，来源为上游 `gemini-3-1-pro-preview`），仅在本地/同步/手填均缺失时生效。
- **回归测试**: 新增 `TestMatchPriceNormalizesVariants` 覆盖上述 8 种写法的匹配与目标键。

## v0.11.13 (2026-10-04)

### Fixes（按一轮完整前后端审查逐项修复）
- **宿主进程健壮性**: `cliproxyPluginCall` 增加 `defer recover()` —— 任何 handler panic 不再跨 cgo 边界终结 CPA 宿主进程，而是返回错误信封并计入 `plugin_panics`。
- **数据库初始化失败不再静默**: `ensureDB` 由 `panic` 改为记录错误，健康接口返回 `db_init_error`、`storage.status=unavailable` 并产生 error 级告警，不再"静默服务空数据"。
- **事件页时间范围失效**: 前端 `/events?limit=500` 被 `fetchJSON` 追加 `?range=` 拼成 `?limit=500?range=…`，`range` 丢失 → 事件页永远按 30 天取数。改为 `fetchJSON("/events",{limit:500})`。
- **事件页无分页 + 客户端筛选**: 新增 `limit/offset` 分页控件，并把状态/来源/搜索全部下推服务端（新增 `executor`、`failed`、`q` 查询参数），不再只在最早加载的一页里过滤。
- **真实状态码**: `/api/events` 补齐 `failure_status_code`、`reasoning_tokens`、`cached_tokens`、`cache_read/creation_tokens`、`ttft_ms`、`source`、`service_tier`；前端状态徽章不再硬编码 `OK 200`/`FAIL 429`。
- **NULL 列导致事件消失**: events 查询全部改用 `COALESCE(...)`，NULL 列不再让整行被静默跳过。
- **时区/DST 正确性**: 所有时间范围过滤与排序改用 `datetime(timestamp)`（归一化到 UTC），并新增表达式索引；跨时区偏移的历史行不再错排/漏查。清理任务的过期判断同样修正。
- **价格不可见的静默 $0**: 健康接口新增 `price_sync`（含失败原因）与 `unpriced_models`，同步失败或存在无定价模型时会给出告警；`getPriceSyncStatus()` 此前从未被调用。
- **`/summary` 响应形态分裂**: `/api/summary` 恒定返回 summary 结构，Quotio 结构改由 `/api/usage` 专用处理，调用方漏传 `range` 不再静默换 schema。
- **观测/健壮细节**: `/api/timeseries` 增加 ETag（含数据版本，避免每次刷新重跑 12 条聚合）；`offset` 增加上限。
- **前端错误可见性**: 新增面板级错误横幅 + 重试按钮，各 tab 的抓取失败不再等同于"无数据"；配额面板失败时不再永久空白；新增加载占位、搜索 debounce、窗口 resize 重绘图表、详情抽屉真实状态码。
- **无障碍**: tab 补齐 `aria-controls`/`aria-labelledby` 与方向键/Home/End 导航（roving tabindex）。
- **清理死代码**: 删除从未被页面加载的 `dashboard/helpers.js` 及其测试（原测试只覆盖未上线代码），同步移除 CI 中对应的 `node --check`；把未被 CI 收集的 `normalize_test.js`/`aggregate_test.js` 重写为 `*.test.js`，并改为从 `template.html` 提取真实函数进行覆盖。

## v0.11.12 (2026-10-04)

### Fixes
- **「模型开销 Top 5」空白不渲染**: 帕累托改版中，绘图点集 `pts` 缺少 `item` 字段，而标签循环仍读取 `p.item.model`，导致 `renderTopModels` 抛出 TypeError 并在写入 DOM 之前中断 —— 卡片一片空白，且同一批调用里随后执行的「系统健康与运行洞察」也一并被跳过。已补回字段。
- **补充运行时回归测试**: 新增 `dashboard/render_top_models.test.js`，用最小的 DOM stub **真实执行** `renderTopModels()`（含单模型、空数据边界），静态字符串断言无法发现这类运行时异常。

## v0.11.11 (2026-10-04)

### Features
- **「模型开销 Top 5」改为帕累托累计占比曲线**: 由开销绝对值曲线改为累计占比曲线 —— X 轴为开销降序的前 5 个模型、Y 轴为占总开销的累计占比（0→100%，固定刻度便于横向比较），曲线仍采用单调三次平滑；每个点标注累计占比，底部附一行摘要（Top 5 合计占比 · 头部模型占比），悬停可见该模型金额、自身占比与累计占比。

## v0.11.10 (2026-10-04)

### Features
- **「模型开销 Top 5」改为丝滑曲线图**: 由原来的横向进度条列表改为开销排行曲线 —— X 轴为开销降序的前 5 个模型、Y 轴为开销金额，采用与主图一致的单调三次平滑（`smoothPath`）绘制面积 + 折线 + 数据点，点上标注金额、轴下标注模型名；悬停数据点可查看完整模型名、provider、请求数与 Tokens。

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
