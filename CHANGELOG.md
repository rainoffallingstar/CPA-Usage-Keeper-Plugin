# Changelog

## v0.12.11 (2026-10-26)

### Fixes
- **Ollama Cloud 刷新需要点两次才生效**：仪表盘调用的是 `GET ?action=refresh`，而 Ollama 的这条分支直接 `go refreshOllamaQuota(...)`，**没有走 `beginRefresh`**，因此列表里的 `refreshing` 永远是 `false`。前端第一次轮询就判定"已完成"并渲染旧数据；等第二次点击时后台那次抓取刚好落地，才看到新值。
  - Ollama 的 GET / POST 两条 refresh 分支均补上 `beginRefresh` + `defer endRefresh`。
  - 同类问题一并修复：**Colab** 的 GET refresh 分支、**OpenCode** 的 GET/POST refresh 分支（OpenCode 是同步刷新，不需要轮询，但补 guard 后可防止双击并发抓取）。
  - 新增最小可见忙碌时间（650ms）：Ollama 抓取常在 0.5s 内完成，若不等待，spinner 只闪一帧、按钮看起来"没反应"。现在会稳定显示「同步中…」再切换为「已更新」。

### Tests
- `TestEveryProviderRefreshPathRegistersInFlight`：遍历六个 provider 中**每个 `case "refresh"` 分支**，要求分支体内出现 `beginRefresh`（允许 POST 分支委派给 GET handler）。该测试在把修复回退后能稳定复现失败，已验证其有效性。

### Verified
- 真实环境单次点击：`refreshing=true`（0.02s）→ 0.74s 后清除并带新 `updated_at`；浏览器侧按钮 200ms–1500ms 保持「同步中…」且禁用，约 2.5s 恢复，Session 数值由 21.9% 更新为 22.7%。

## v0.12.10 (2026-10-23)

### UI
- **收紧纵向节奏，消除 Tab 栏与内容之间的空洞**：合并后的单栏头部下方原本留 52px 才开始页面标题，配合 32px 的标题下边距、48px 的区块分隔间距，整体偏「营销页」而非数据看板。
  - 头部→标题：**52px → 28px**；标题块下边距 32px → 22px；eyebrow 与标题间距 10px → 7px。
  - KPI 卡槽下边距 48px → 26px；区块分隔线上下 48/24px → 26/16px；卡片网格间距 24px。
  - 卡片内边距：头部 22/26px → 18/22px，正文 14/26/26px → 12/22/20px；KPI 卡 24/24/20px → 20/22/16px，标题行距 18px → 12px。
  - 移动端同步收紧（主区 18px 起、卡内 16/18px、正文下边距 16px）。
  - 实测整页高度 1430px → 1252px（-12%），在 1440×900 下首屏可完整看到 KPI 与主图表。

## v0.12.9 (2026-10-23)

### UI
- **头部与 Tab 栏合并为一条**：原来是两条独立的吸顶栏（56px 品牌/控件 + 48px Tab），中间还有分隔线，视觉上割裂且占掉 104px 高度。
  - 现为**单条 60px 网格栏**：`品牌 | Tab | 控件` 三段式，Tab 用下划线指示器直接坐在栏底，控件右对齐。
  - 空间不足时按优先级让位：≤1180px 先隐藏「停止刷新」下拉；≤980px 隐藏状态胶囊并收紧 Tab 间距；≤700px Tab 换到第二行（品牌+控件在上），仍属同一条 sticky 栏，总高 56px+40px。
  - Tab 溢出时用头部伪元素做右缘渐隐提示（`mask-image` 在横向滚动容器里会裁切内容而非提示，故改在不会滚动的 header 上）。
  - 同步移除残留的 `.nav-tabs-wrapper` 规则与 5 处过期样式，`.main-content` 的上边距与 `scroll-margin` 按新栏高重新校准。

### Fixes
- 合并过程中再次引入 3 个未转义的裸 `%`（`height:100%`），`TestRenderDashboardHasNoFormatArtifacts` 在构建前即拦下；该测试已成为模板改动的强制门禁。

## v0.12.5 (2026-10-23)

### Design
- **仪表盘视觉重构（award-tier pass）**：以 Editorial data product 为方向重做视觉系统，逐项对照排版 / 留白 / 层级 / 色彩 / 动效 / 微交互 / 响应式 / 原创性八个维度。
  - **令牌层**：重建颜色语义（`--surface-sunken`、`--hairline`、`--tint-accent`）、四级阴影 `--elev-1..3`、字阶（含 `--text-display` 流体标题）、字距（`--tracking-*`）、弹簧曲线 `--ease-spring` / `--ease-out-expo`、8pt 间距刻度。浅色背景改为 `#fbfbfc` 以让白色卡片浮起；深色改 `#0a0a0c` 分层。
  - **排版**：新增 eyebrow + page-title + lede 的编辑式页头，`text-wrap:balance/pretty` 避免孤行；表格数字统一 tabular-nums 与负字距；KPI 数值用 clamp 流体字号并收紧字距。
  - **层级**：KPI 由「四张独立卡」改为**一体化卡槽**（1px 分隔线共享边框），数字成为页面最强视觉锚点；主内容区留白从 24px 提升到 48px；区块间加入 section rule 与序号化 eyebrow。
  - **导航**：药丸选中态改为**下划线滑入**（`tabUnderline`），图标统一 1.8 描边；窄屏加右缘渐隐提示可滚动。
  - **动效**：卡片分组 `reveal` 错峰入场（60–240ms），切 Tab 重新播放；KPI 数字 620ms 缓动计数；进度条改用 `ease-out-expo` 慢出。
  - **微交互**：按钮按下 `scale(0.965)` + 弹簧回弹，主按钮带柔和投影；卡片 hover 抬升 2px 并叠加极淡径向高光；焦点环统一为 3px 半透明 accent。
  - **响应式**：≤700px 时 KPI 改为「标题 / 数值 + 右侧迷你图 / 副标题」三区网格，单卡高度减少约 35%；头部精简并隐藏冗余状态胶囊；状态条改为上下两段。
  - **健康页**：把孤立的「系统运行状态: OK」内联标题换成 status strip（状态 + Uptime/队列/写入/未定价四项 meta），移除两条无阈值的伪进度条。

### Fixes
- **模板裸 `%` 导致整页脚本失效**：新增的 `linear-gradient(… 72%)` 未转义，`fmt.Sprintf` 把它编译成 `%!)(…)`，浏览器抛 `SyntaxError: Unexpected token '%'`，整个仪表盘白屏。已修正，并新增 `TestRenderDashboardHasNoFormatArtifacts`——把模板真正过一遍 `fmt.Sprintf`，用 `%!x(` 模式精确识别 fmt 产物（不会误伤 CSS 的 `100%!important`），同时断言两个 `%d` 已被替换。该测试同时挖出 3 处历史遗留的裸 `%`。
- **导航脱离吸顶并压住标题**：重构时误将 `.nav-tabs-wrapper` 从 `sticky` 改为 `relative`，导航随文档流滚动并覆盖页头。已恢复 `sticky` 并使其 `top` 与 57px 头高对齐；用 CDP 实测 header 0–57 / nav 57–105 / 内容 161 起，无重叠。
- **双重下划线指示器**：新旧 `.nav-tab-btn.active` 同时生效（后者依赖源码顺序），旧规则仍在加背景与投影。已合并为单条规则。
- **KPI 徽标大写破坏单位**：`text-transform:uppercase` 把 `12.8s` 显示成 `12.8S`，已移除。
- **走势图 Y 轴被裁切**：左侧 gutter 50px 装不下等宽 `$297.70`，`$` 被切掉；加宽至 64px。

### Tests
- `TestRenderDashboardHasNoFormatArtifacts`（Go，真实 fmt 渲染）。
- dashboard 冒烟断言更新：健康页文案改为「运行正常」，其余 41 项全通过。

## v0.11.45 (2026-10-23)

### Fixes
- **给其他异步按钮补上同样的防重复与进度反馈**：v0.11.44 只处理了配额卡片的「刷新配额」。排查后发现一批按钮同样是"点下去毫无反馈"，其中几个还会**重复写入**。
  - 新增 `withBusy(btn, text)` 统一辅助：首次调用禁用按钮、加 `aria-busy` 与旋转图标；重复调用返回 `null`，调用方直接忽略本次提交；`restore(doneText)` 恢复并在成功后短暂显示完成文案。
  - 接入的按钮：
    - **「保存账号」**（OpenCode / GLM / Ollama / DeepSeek / NewAPI，含"更新凭据"）——原来可连点导致重复创建或重复改凭据。
    - **「移除账号」**——原来一点即删、可连点，且凭据会一并删除；现在先 `confirm` 确认再执行。
    - **「添加规则」/「保存规则」/删除定价「×」**——删除也加了确认。
    - **Key 列表「刷新」**——按钮进入「刷新中…」，完成或失败都会复原。
    - **Colab 7/30/90 天切换**——加载期间整组禁用，避免两个响应竞争同一张图。
    - **顶部「立即刷新」**——按钮禁用直到 `refreshAll()` 完成。
  - 表单校验失败时先复原按钮，不会出现"按钮卡在禁用态"。

### 说明
- 已经具备加载态的按钮保持不变：「同步定价」「确认重置」「提交回调」「登录 Colab」。

### Tests
- `Test withBusy`：首次获取锁、第二次被拒、完成后可再次使用。
- `removeQuota` 必须包含 `window.confirm` 与 `withBusy`（源码断言）。
- `withBusy` 对无 `dataset` 的桩元素容错，避免主题测试里的异步调用抛错。

## v0.11.44 (2026-10-23)

### Fixes
- **配额卡片「刷新配额」没有任何反馈，用户只能反复点击**：刷新在后端是异步的（`go refresh…` 立即返回旧数据），前端拿到响应就重渲染，数字没变化、按钮也没变化，看起来像没点到。
  - **按钮立即进入加载态**：禁用 + `aria-busy` + 旋转图标 +「同步中…」，完成后短暂显示「已更新」再复原。
  - **轮询到真正完成**：每 900ms 读取一次该 Provider 的列表，直到后端不再报告 `refreshing` 才渲染并恢复按钮，最多约 22 秒后提示「同步超时」。
  - 新增 `refresh_guard.go`：按 `provider|account` 去重，重复点击不会并发发起多次上游请求（此前每次点击都会起一个 goroutine，互相覆盖缓存，结果看起来随机）。并发安全由 `TestBeginRefreshIsSafeUnderConcurrency` 覆盖。
  - 各 Provider 列表响应新增 `refreshing` 字段（NewAPI / DeepSeek / GLM / Ollama / Colab），供前端判断何时结束等待。
  - 顶部「同步配额」按钮同样进入加载态，并等待 `renderQuota()` 完成。

### Tests
- `TestBeginRefreshDeduplicatesConcurrentClicks`、`TestBeginRefreshIsKeyedPerProviderAndAccount`、`TestBeginRefreshIsSafeUnderConcurrency`。
- 前端：刷新按钮必须立即禁用并显示「同步中」，且轮询到后端清除 `refreshing` 后才恢复并渲染。

## v0.11.43 (2026-10-21)

### Fixes
- **重置后网页看不到变化（三个独立原因）**：
  1. **页面从未重新加载**：插件日志显示最后一次加载 Dashboard 是 18:32，而两次重置发生在 19:43 / 19:45；`refresh_seconds: 0` 时不自动刷新，切走再切回配额页才会重拉数据。已在 Key 弹窗新增「刷新」按钮，并在重置成功后同时刷新 Key 列表与配额卡片。
  2. **HTTP 缓存**：所有插件 JSON 响应只有 `Content-Type`，没有缓存控制头，浏览器可对 200 GET 做启发式缓存；`/newapi-quota`、`?action=keys` 都是实时读数。现在统一返回 `Cache-Control: no-store, no-cache, must-revalidate` + `Pragma: no-cache`，前端对配额端点额外使用 `cache:"no-store"` 并附 nonce。
  3. **`omitempty` 吞掉 0**：重置后 `cycle_used_usd` 正好为 0，`omitempty` 会让该字段整个消失，客户端读到 `undefined`——偏偏在最需要它的时刻。`cycle_active` / `cycle_total_usd` / `cycle_used_usd` 去掉 `omitempty`，消费端以 `cycle_active` 判断是否渲染本轮视图。

### 说明
- 你 19:43 与 19:45 的两次重置**均已成功**：面板审计记录为 `token.update changed_fields: ["remain_quota"]`，本地 `newapi_key_cycles` 中 `team` 的记录为 `baseline_used=73959135`、`cycle_total=102500000`（$205）、`reset_at=2026-10-21T11:45:13Z`，接口返回 `remaining=$205.00`、`cycle_total=$205`、`本轮已用 $0.00`。

### Tests
- `TestBuildNewAPIKeysKeepsZeroCycleUsed`：断言 `cycle_used_usd:0` 必须出现在 JSON 中，且 `cycle_active` / `cycle_total_usd` 同时存在。

## v0.11.42 (2026-10-21)

### UI
- **Key 行改为「进度条上下各一行文字」**：meta（分组 / 最近使用 / 本轮起算）在进度条上方，数字统计（本轮已用 / 剩余 / 累计已用）在进度条下方，两行均左对齐。进度条不再被两行文字夹在右对齐与右对齐之间，整列起始位置一致，扫读时视线只需沿左侧一条竖线移动。
- 进度条与两行文字的间距统一为 6px；统计组仍使用 `flex-wrap`，仅在窄屏时换行，且换行后依然左对齐。

## v0.11.41 (2026-10-21)

### Fixes
- **API Key 行文字错位**：数字用量原先被 `justify-content:flex-end` 推到行尾，左侧留下大片空白，且重置按钮单独占一整行，导致每张卡片的行宽与起始位置参差不齐。
  - 用量改成**左对齐的 label/value 统计组**：`本轮已用 $0.00 / $105.00` `剩余 $105.00 (¥714.00)` `累计已用 $101.77`，每组内标签与数值紧邻，组间 20px 间距，窄屏自动换行。
  - 重置按钮移回**标题行右侧**（与状态徽标同一行），卡片正文不再出现孤立按钮行。
  - 分组/最近使用/本轮起算等信息保留为一行 meta，超长时省略号截断（完整内容见 `title`）。
  - 「累计已用」在重置态下补回统计组，避免重置后看不到历史消耗。

## v0.11.40 (2026-10-21)

### Fixes
- **Key 行的用量被拆成两行**：meta（分组/最近使用/累计已用）与数字用量挤在同一个 `display:flex` 行内，加入面板货币后缀后内容变长，用量被挤到第二行。现在两者各占一行：meta 单独一行并做省略号截断（完整内容在 `title` 中），用量行右对齐且 `white-space:nowrap`，数字不会再被折行。
- **括号里的面板货币金额对错了对象**：`(¥692.04)` 实际是**累计已用** $101.77 × 6.8 的折算，却被贴在「余 $105.00」后面，看起来像余额的 CNY 值（应为 ¥714.00）。现在有额度上限的 Key 使用 `panel_remaining`（剩余额度的 CNY 折算），只有不限量 Key（没有余额）才用 `panel_used` 标注累计消耗。

### Tests
- `TestBuildNewAPIKeysPanelAnnotationMatchesBalance`：剩余 $105.00 → `¥714.00`，累计已用 $101.77 → `¥692.04`，两者不得混用。
- 前端断言：出现 `(¥102.00)`（余额折算），不得出现按累计已用折算的金额。

## v0.11.39 (2026-10-21)

### Fixes
- **点击「重置额度」后没有任何反应**：确认弹窗与 Key 列表弹窗都使用 `.modal-overlay` 的 `z-index:2500`，而 Key 列表在 DOM 中更靠后，同层级按文档顺序绘制，导致确认弹窗被压在下面——按钮点不到，请求根本没有发出（CPA 访问日志中无 `action=resetkey` 记录）。
  - 新增 `.modal-overlay-stacked { z-index: 2600 }`，重置确认弹窗使用该class，确保叠在被它唤起的 Key 列表之上。
  - 新增回归断言：`.modal-overlay-stacked` 的 z-index 必须严格大于 `.modal-overlay`。

## v0.11.38 (2026-10-21)

### Features
- **重置后按“本轮”重新计算进度**：重置只会覆盖面板的 `remain_quota`，而上游的 `used_quota` 是终身累计、永不归零，因此重置后进度条会长期显示“已用 / (已用+剩余)”，看起来几乎耗尽。
  - 新增 `newapi_key_cycles` 表，在重置校验通过后记录 `baseline_used`（写入前的累计已用）、`cycle_total`（本次授予额度）、`reset_at`。
  - 卡片进度条改为：总长 = 本轮授予额度，已用 = `used_quota_now - baseline_used`，剩余 = 面板返回的 `remain_quota`（权威值）。
  - **累计已用依旧展示**：meta 行固定显示 `累计已用 $X`，并额外标注 `本轮起算 YYYY-MM-DD`；列表头部汇总也改称「累计已用」，避免与「本轮」混淆。
- **面板侧补充额度时自动重定基线**：若当前余额显著超过记录的 `cycle_total`（说明在面板网页端或其他工具加过额度），自动以当前余额为新一轮起点，避免画出「剩余大于总额」的矛盾进度条。
- 只在校验通过后写 cycle，失败/被覆盖的重置不会留下虚假的本轮记录；Key 未记录 cycle 时回落到终身 `已用/总额`。

### Tests
- `TestResetRecordsCycleAndReportsCycleUsage`（重置写入 baseline 与 cycle_total；面板继续累加后，本轮已用 10/50、剩余 40，累计已用仍为原值）。
- `TestBuildNewAPIKeysRebasesStaleCycle`（面板侧加额度后自动重定基线，进度条自洽）。
- `TestBuildNewAPIKeysWithoutCycleFallsBackToLifetime`。
- 前端：断言出现「本轮 $5.00 / $20.00」「累计已用 $65.00」「本轮起算 2026-10-21」，且进度条按本轮 25% 而非终身值渲染。

## v0.11.37 (2026-10-21)

### Fixes
- **重置额度的预填值被四舍五入**：弹窗此前用 `toFixed(2)` 预填，`$101.103528` 会变成 `$101.10`，直接确认会写入 `50,550,000` 而非原值 `50,551,764`，静默少 1,764 个额度单位。现在后端在 Key 列表中返回原始计数器 `remaining_quota`，弹窗按 `remaining_quota / quota_per_unit` 以最多 6 位小数预填，未修改即确认可精确复现原值（面板审计为 `changed_fields: []`）。
- **换算改为四舍五入而非截断**：`101.103528 × 500000` 在浮点下可能得到 `50551763.99999999`，截断会丢 1 个单位；后端改用 `math.Round`。
- 新增 `quota_usd` 上限校验（≤ 1,000,000），避免误输入天文数字。

### Improvements
- 弹窗实时显示换算过程：`写入值 = 50,551,764 额度单位（$101.103528 × 500,000） · 面板显示 ¥687.50`，并展示当前「额度单位 / USD」，写入量不再隐式。
- Key 列表接口返回 `quota_per_unit` / `display_type` / `exchange_rate`，前端不再硬编码汇率。

### Docs
- `doc/quota-providers.md` 新增「重置金额如何计算」小节：公式、dddai 实测换算表、两条精度规则。

### Tests
- `TestResetNewAPIKeyQuotaExactUnits`（`101.103528→50551764`、`101.10→50550000`、`0.000002→1` 等）。
- 前端：预填必须保留 `101.103528`、预览含 `50,551,764` 与 `¥687.50`，且 `101.10` 预填会少 1,764 单位。

## v0.11.36 (2026-10-21)

### UI refinement
- NewAPI Key 列表：把「重置额度」按钮从标题行移到**该 Key 独立的动作行**（右对齐），避免与状态徽标并排造成归属歧义；不限量 Key 依然不显示该按钮。
- 确认弹窗措辞与面板审计口径对齐：明确写出「只修改『剩余配额』这一个字段」，并提示审计日志中应只出现该变更。

### Notes
- 与网页端手动重置的审计记录一致：面板网页端显示 `修改字段：剩余配额`，本插件的等值重置在审计中为 `changed_fields: []`（写入值与已有值相同），说明确实只触碰了余额字段。

## v0.11.35 (2026-10-21)

### Features
- **NewAPI：单个 API Key 一键额度重置**：Key 列表为**有额度上限**的 Key 增加「重置额度」按钮（不限量 Key 不显示）。弹窗展示当前已用/剩余并预填当前余额，确认后写回线上配置。
  - 上游没有独立的重置端点，只能 `PUT /api/token/`，而该接口会**用请求体覆盖全部可编辑字段**（名称、状态、过期时间、余额、不限量、模型限制、IP 白名单、分组、跨组重试）。因此实现为严格读-改-写：先 `GET /api/token/:id` 取全量 → 只改 `remain_quota` → 其余字段原样回传 → 复读校验余额与未改动字段。
  - 额度耗尽（`status=4`）的 Key 会在余额写入成功后再发一次 `?status_only=1` 恢复启用：上游拒绝在余额仍 `<=0` 时启用令牌，顺序不能颠倒。
  - `used_quota` 是只读列（不在上游更新列清单内），重置不会清零累计用量。
  - 金额以 USD 输入并按 `quota_per_unit` 换算，前端不接触内部额度单位；重置值需 > 0。
  - 权限：除 `api_key:read` 外还需 `api_key:write`；缺权限时把面板原文（`ACCESS_TOKEN_SCOPE_DENIED … api_key:write`）原样展示。

### UI refinement
- 重置按钮从 Key 标题行移到**该 Key 自己的动作行**（右对齐、独立一行），避免与状态徽标挤在一起导致归属歧义；不限量 Key 仍不显示任何动作。

### Verified
- 对 `team`（`sub-openai`，模型限制 `gpt-6.1-sol,gpt-6-sol,gpt-5.6-luna,gpt-6-luna,gpt-6-astra`）执行等值重置，写回后 `name` / `group` / `model_limits` / `model_limits_enabled` / `expired_time` / `allow_ips` 全部保持不变，`used_quota` 未变，余额与请求值一致。
- 开发期曾用 `{"id":9876}` 探测写权限，触发了上游的覆盖式写入（`name`/`model_limits`/`group`/`expired_time` 被清零），已立即按原值恢复并复核。该事故正是本次改为全量读改写的原因。

### Tests
- `TestResetNewAPIKeyQuotaPreservesEveryOtherField`（模拟上游覆盖式写入，断言名称/分组/模型限制/过期时间/IP 白名单/累计用量均未被破坏）。
- `TestResetNewAPIKeyQuotaRevivesExhaustedKey`（余额写入必须早于启用请求）。
- `TestResetNewAPIKeyQuotaRejectsUnlimitedAndBadInput`、`TestResetNewAPIKeyQuotaSurfacesScopeDenied`（失败写入不得改变状态）、`TestHandleNewAPIResetKeyValidatesInput`。
- `dashboard/init_smoke.test.js`：重置按钮只出现在有额度上限的 Key 上；确认弹窗含写入警示；非法金额不得发起请求。

## v0.11.34 (2026-10-19)

### Fixes
- **模型用量页显示"当前时间段暂无模型用量记录"，实际是查询失败**：生产库的 `idx_usage_events_model` 索引损坏（`PRAGMA quick_check` 报 `wrong # of entries in index idx_usage_events_model`），凡是查询计划命中该索引的语句都返回 `database disk image is malformed`。`handleModels` 把 SQL 错误静默吞掉后返回 `200 []`，于是前端把"查询失败"渲染成了"这段时间没有请求"，而总览/时间序列（走 `idx_usage_events_dt_id`）一切正常，极具误导性。
  - `handleModels` 现在检查 `rows.Err()`，失败时返回 **500 + 具体错误**，并写入 `models_query_failed` 错误环形缓冲；
  - 前端 `renderModels()` 在请求失败时渲染「模型用量查询失败」+ 重试按钮，**不再回落到空状态**。
- **损坏索引自动修复**：`runSQLiteMaintenance` 每小时新增 `PRAGMA quick_check`，发现结构异常时逐个 `REINDEX` 四个索引并复检；修复成功即清除告警。索引损坏时表数据完好，`REINDEX` 不触碰任何行。
- **健康页暴露完整性状态**：`storage.integrity_ok` / `storage.integrity_error`，异常时同时产生 `db_integrity` 错误级告警，健康页「存储与落盘性能」卡片新增「数据库完整性」徽标。

### Verified
- 现场库执行 `REINDEX idx_usage_events_model` 后 `quick_check: ok`；`/api/models?range=30d` 由 `[]` 恢复为 14 个模型（`deepseek-v4.1-flash` 36377 次 / 76.6 亿 tokens 等），1h/24h/7d/30d 四个区间均正常。
- 修复前已把数据库备份到 `.deploy-bak/usage-keeper.db.before-reindex`。

### Tests
- `TestHandleModelsReportsQueryFailureInsteadOfEmptyList`（关闭句柄 → 断言 500 + error 字段 + 错误环形缓冲有记录）。
- `TestCheckAndRepairIndexesLeavesHealthyDatabaseAlone`、`TestHealthExposesIntegrityFlag`。
- `dashboard/init_smoke.test.js` 新增「查询失败必须渲染错误态、不得渲染空状态」断言。

## v0.11.33 (2026-10-19)

### Fixes
- **不限量 Key 显示负余额**：上游对 `unlimited_quota=true` 的令牌把 `remain_quota` 记为负数（它是记账计数器，不是余额）。实测 `oui`/`oui2`/`akile` 均为负值，此前直接换算会把「余 $-…」显示出来。现在不限量 Key 的剩余额度统一归零、只展示「已用 + 不限量」，且对任何负值做兜底钳制。
- **Key 列表排序**：改为按已用金额降序（同额时按 id 降序），最耗用的 Key 排在最前，符合用量视图直觉。
- **面板货币补充**：Key 行在 USD 之后追加面板本地货币（如 `(¥260.58)`），与订阅卡片口径一致。

### Verified
- dddai 令牌补齐 `api_key:read` 后实测：`GET /api/token/` 返回 5 个 Key，插件输出全部字段正确，无负余额；原始响应中的 `key` 字段（18 字符）未进入解码结构体，响应 JSON 与前端渲染均不含 `sk-`/明文。

## v0.11.32 (2026-10-19)

### Features
- **NewAPI：API Key 列表（仅名称与用量）**：配额卡片新增「API Keys」按钮，列出该面板账号下的全部 Key，显示名称、启用/禁用/过期/耗尽状态、分组、最近使用、到期时间、已用/剩余/总额（USD + 面板本地货币）与使用率进度条，并给出"共 N 个 / 已用合计 / 余额合计"汇总。

### Security
- **完全不解码 key 明文**：`GET /api/token/` 的上游响应含 `key` 字段（上游已做掩码，但我们不做任何依赖），插件结构体 `newAPITokenItem` **不声明 `key` 字段**，JSON 解码时直接丢弃；既不写入内存缓存、不落 SQLite，也不进入 API 响应与 Dashboard。不调用任何 reveal 接口（`POST /api/token/:id/key`）。
- 令牌仍只从请求头读取（`X-NewAPI-Token`），避免 CPA 访问日志泄露。
- 单页 100 条自动翻页（上限 100 页），复用 `/api/status` 的 `quota_per_unit` 与显示货币设置做换算。

### Notes
- 读取 Key 列表需要令牌具备 `api_key:read` 权限；缺失时弹窗显示面板原文 `ACCESS_TOKEN_SCOPE_DENIED … api_key:read`，不影响配额卡片本身。
- 当前 dddai 令牌未授予该权限，实测返回 403，属预期；在面板「设置 → 安全设置」给令牌勾选 API 密钥读取权限后即可看到列表。

### Tests
- `TestFetchNewAPITokensPaginatesAndNeverKeepsPlaintext`（250 条 / 3 页；断言解码后的结构体 JSON 不含 `sk-`/`SECRET`）。
- `TestBuildNewAPIKeysNeverExposesSecret`（正常/不限量/过期/禁用状态，USD 换算，响应不含 `key`/`sk-`/`secret`）。
- `TestHandleNewAPIKeysErrorShape`（缺 `api_key:read` 时返回空列表 + 面板原文提示）。
- `dashboard/init_smoke.test.js` 新增 Key 列表弹窗渲染断言（名称、用量、不限量、隐私提示，且不出现 `sk-`）。

## v0.11.31 (2026-10-19)

### Fixes
- **资源路由 POST 在 CPA 宿主机上返回 404**：宿主的 resource 路由只注册了 GET（其余 Provider 的 POST 同样 404），此前前端用 POST JSON 保存账号实际无法生效。
  - 新增账号 / 更新凭据改为 `GET ?action=…` + **请求头**传参（`X-NewAPI-Token`、`X-NewAPI-Base-URL`）。CPA 会把完整请求 URI 写入访问日志，若把令牌放在查询串会明文泄露到日志、浏览器历史与代理日志，因此凭据只走请求头。
  - 后端同时兼容查询参数（供 curl 调试）与大小写不敏感的请求头；POST JSON 入口保留，供管理 API 使用。

## v0.11.30 (2026-10-19)

### Features
- **订阅配额新增「NewAPI」Provider**：接入 NewAPI 兼容中转面板（dddai 等）的钱包与订阅额度。使用**面板访问令牌**（`nap_...`）而非 `sk-` 模型密钥，面板地址可在 Dashboard 中逐账号配置。
  - **只依赖官方通用接口**：`/api/status`、`/api/user/self`、`/api/subscription/self`、`/api/subscription/plans`、`/api/log/self/stat`。不调用任何 dddai 专有扩展，因此对任意 new-api 部署可用。
  - 一次刷新展示：每个活跃订阅的已用/总额/剩余（套餐名、到期天数、可选重置天数）、钱包余额（含累计已用）、近 7 天日均消耗与合计预计可用天数。
  - 面板内部额度单位按 `/api/status` 的 `quota_per_unit` 换算为 USD；同时按 `quota_display_type` / `usd_exchange_rate` 在状态行附带面板本地货币等值（如 `面板显示 ¥4845.38`）。
  - **支持多订阅并存**：每个订阅对应独立分组、额度不可互通，因此逐条渲染独立进度条并按最近到期排序；同套餐重名时自动追加分组或订阅 ID 区分；`amount_total == 0` 按上游语义视为不限量并以文本呈现。预估窗口只汇总"合计剩余"，且在多订阅时明确标注「各订阅额度不互通」。
  - 支持添加 / 移除 / 更新令牌与面板地址（「编辑凭据」）/ 立即刷新；账号落库 `newapi_accounts`（令牌与 Colab refresh token 一样做轻量混淆），并支持 `newapi_accounts` YAML 预配置。
  - 部分权限缺失（如无 `profile:read`）时不再整卡失败：已取到的窗口照常渲染，缺失项与面板原始错误（`ACCESS_TOKEN_SCOPE_DENIED` 等）列在错误区。
  - 新增资源端点 `/api/newapi-quota`（GET/POST）与管理端点 `/v0/management/usage-keeper/newapi-quota`。

### Compatibility notes
- 核对 `QuantumNous/new-api` `main`（`058c823`）与 `v1.0.0-rc.45`：**`/api/billing/summary` 不存在于官方仓库**，属 dddai 聚合扩展；`/api/user/self/pricing`、`/api/user/messages` 同为 dddai 自研。早期实现曾依赖该聚合接口，现已完全移除。
- 官方 `UserSubscription` 不返回套餐名，仅有 `plan_id`，因此 `plan_title` 由 `/api/subscription/plans` 补齐。
- 已用 dddai 真实令牌实测官方接口链路，与聚合接口输出一致（$712.56 剩余 / $12.07 日均 / 59.1 天）。

### Tests
- `TestFetchNewAPISnapshotUsesOnlyStockRoutes`（stock 服务器，断言绝不调用 `/api/billing/summary`；过期订阅被过滤、按到期排序、套餐名补齐、7 天日均与合并剩余）。
- `TestBuildNewAPIQuotaAccountRendersEachSubscription`（两条订阅各自成条 + 钱包 + 预估、多订阅不互通提示）、`TestBuildNewAPIQuotaAccountDisambiguatesDuplicatePlanTitles`、`TestBuildNewAPIQuotaAccountSingleAndUnlimited`、`TestBuildNewAPIQuotaAccountShowsPanelCurrency`、`TestFetchNewAPISnapshotPartialFailureKeepsWorkingWindows`、`TestFetchNewAPISnapshotAllSourcesFail`、`TestNormalizeNewAPIBaseURL`、`TestNewAPIAccountAddRemoveLifecycle`。
- `dashboard/init_smoke.test.js` 覆盖多订阅卡片（两条订阅进度条、面板货币、base URL、编辑凭据入口、多订阅提示）。

## v0.11.29 (2026-10-15)

### Fixes
- **Colab 订阅登录在远程访问下无法完成**: OAuth `redirect_uri` 是插件宿主机的 loopback 地址（`http://127.0.0.1:<port>`），只有跑插件的机器自身能访问。通过反代 / Tailscale 从**另一台设备**打开面板时，Google 授权页回调到这个 127.0.0.1 会打不开，登录卡死；而界面上那个「手动授权码」回退是**硬编码拒绝**的（提示需 PKCE verifier，实际后端从不接受它），登录接口返回的 `redirect_uri` 也是 `http://127.0.0.1:<port>/...` 字面占位符，用户根本不知道真实端口。
  - 新增**远程回投**流程：登录接口现在返回**真实** `redirect_uri`（实际端口）；用户把授权后地址栏里那个打不开的 `http://127.0.0.1:.../?code=...&state=...` **整段**粘回面板提交，后端用**已保存的 PKCE code_verifier + redirect_uri** 完成换取（`action=manualcode`）。回投的 `state` 会与会话 nonce 校验，防止粘错/串会话。
  - 支持粘贴完整回调 URL 或**仅 code 值**两种形式；`code` 里可能含 `/`、`+` 等字符，回投改走 **POST JSON** 避免 URL 编码破坏。
  - 前端把原本失效的「手动授权码」区块换成醒目的「远程回投」面板：展示真实回调地址，并说明本机/远程两种情形；页面刷新丢了会话时，提交回调会自动重新发起一次登录以获得配套 PKCE。

### Tests
- `TestParseColabCallback`（完整 URL / 裸 code / 无 scheme / state 在 code 前 / 百分号编码 / 空串）、`TestColabLoginSubmitCode`（绑定会话、state 校验、未知会话、缺 code 拒绝）、`TestColabLoginResponseHasRedirectURI`（返回真实端口而非 `<port>` 占位）。

## v0.11.28 (2026-10-15)

### Fixes
- **`models_without_price` 对包月占位模型误报**: 健康页报「1 model(s) with token usage have no price - their cost shows as $0」，查证为 `cursor/default` —— `cursor` 提供商（包月订阅、不按 token 计费）把请求统一记为占位名，上游价格源里根本没有、也永远不会有它的条目。
  - 新增**非计费模型豁免**：`isNonBillableModel` 识别内置表 `nonBillableModels`（当前含 `cursor/default`）、占位名后缀（`/default`、`/auto`、`/unknown`）以及新增配置项 `unpriced_ignore_models`；`countUnpricedModels` 跳过这些名字，不再计入未定价统计。成本仍显示 $0（包月无 token 单价，这是正确结果）。
  - 新增 `unpriced_ignore_models`（字符串/列表，逗号或换行分隔）配置项，日后出现新的包月占位模型无需改代码即可豁免。
- **价格同步失败后「最近错误」长期残留**: 同步失败会 `recordError("price_sync_failed", …)` 写入错误环，但后续同步成功后**从不清理**，于是昨天的一次瞬时失败在今日同步已成功后仍一直显示。
  - 新增 `clearErrorsByCode`，`doPriceSync` 成功后清除 `price_sync_failed` 残留条目，健康页「最近错误」不再展示已恢复的故障。

### Improvements
- **价格同步更抗上游冷启动抖动**: 上游价格源每次请求都冷渲染（`cache-control: no-store` / Vercel MISS），实测稳态 ~3s、**冷启动尖峰达 46.8s**，超过原 45s 客户端超时。
  - 客户端超时 45s → **90s**（并加 `ResponseHeaderTimeout`），低于前端 120s `AbortController` 兜底；
  - 重试由「1 次、固定 3s」改为 **3 次、2s/8s 指数退避**；
  - 请求头显式 `Accept-Encoding: gzip`，549KB 正文压缩传输，降低「等待响应头超时」概率。

### Tests
- `TestCountUnpricedModelsSkipsNonBillablePlaceholderModels`、`TestIsNonBillableModel`：包月/占位名豁免、真正未定价的计费模型仍计数。
- `TestClearErrorsByCodeDropsMatchingEntries`：同步成功后仅清除 `price_sync_failed`、保留其它错误。

## v0.11.27 (2026-10-10)

### Fixes
- **插件身份元数据仍指向上游仓库**: `registry.json` 与 `pluginRegistration()` 里的 `author` / `repository` 还是脚手架残留的 `router-for-me` / `https://github.com/router-for-me/cpa-plugin-usage-keeper`。前者是插件市场清单（`registry.json` 的 version 已在 v0.11.26 同步），后者会随注册信息返回给 CPA、显示在插件列表里。现统一改为本仓库 `rainoffallingstar` / `https://github.com/rainoffallingstar/CPA-Usage-Keeper-Plugin`。
  - 未改动：`go.mod` 的 `module github.com/router-for-me/cpa-plugin-usage-keeper`（Go 模块路径，对 c-shared 构建无影响，改它属于一次重命名决策）；对 CLIProxyAPI SDK 的 `github.com/router-for-me/CLIProxyAPI/v7` 引用是宿主项目本身，保持不变。

## v0.11.26 (2026-10-10)

### Fixes
- **紧急修复：v0.11.25 让仪表盘整页空白**。v0.11.25 的平滑代码里写了 `win%2`，而 `dashboard/template.html` 是经 `fmt.Sprintf(dashboardHTML, cfg.RetentionDays, cfg.RefreshSeconds*1000)`（见 main.go）处理的 printf 模板——**所有字面量百分号必须写成 `%%`**。这个落单的 `%2` 被 Go 当成格式动词，服务端输出成 `win%!?(int= 0)win:win+1`，导致整段 `<script>` 语法错误、页面全白。现改用位运算 `win&1`（不含 `%`），行为不变（窗口取奇数）。
  - 说明：字面量 `%` 若写成 `%%` 则安全（如 `idx%%2`、`width:100%%`）；只有**落单**的 `%X` 才会被 fmt 吞掉。

### Tests
- `dashboard/colab_history_chart.test.js` 新增 **printf 安全守卫**：扫描 `template.html` 源码，要求每个 `%` 只能是 `%%` 或 `%d`——正是这条测试会在将来第一时间拦住同类「整页空白」错误。

## v0.11.25 (2026-10-10)

### Improvements
- **Colab「用量历史」曲线改为平滑趋势**: 余额序列本质是「阶梯形」——配额只在离散时刻变化（30 天内 5000 个快照仅 286 次数值变动），平台很长、跳变近乎瞬时（相邻快照相距约 3 分钟）。原先对 5000 个原始点直接套保形三次样条，跳变在时间轴上几乎垂直，任何样条都无法把它画圆，于是呈现锯齿状阶梯。现改为先把历史重采样到等间隔网格（约每小时 1 点，上限 160 点），再做**两遍居中滑动平均**（≈三角核，单遍只会把跳变变成直线段），最后仍用保形三次样条绘制：跳变被柔化成圆滑的 S 形过渡，观感为平滑曲线。纵轴刻度仍按**原始**最小/最大值计算，量程不因平滑而改变。
  - 说明：平滑属**趋势展示**——过渡段画的是插值余额，而非真实瞬时值；平台段与量程忠于原始数据。

### Tests
- `dashboard/colab_history_chart.test.js` 新增：800 个阶梯快照必须被重采样/平滑到 ≤200 段路径（防止回退成逐点绘制）。

实机验证：真实 5000 点下曲线路径由 176874 字符（阶梯）→ 5648 字符（平滑），圆角过渡在 headless Chromium 中确认。

## v0.11.24 (2026-10-10)

### Fixes
- **Colab「用量历史」从未真正画出曲线**: 用户反馈历史卡片只有网格、纵轴刻度和图例，**没有数据线**（横轴日期还挤在左边缘）。根因在 `renderColabHistoryChart`：它在写入 `<svg>` 之后用 `chartBox.innerHTML += ...` 逐段追加 `<path>` 与 `<text>`。`+=` 是把已有 DOM 序列化成字符串再整体重新解析，于是追加进来的 `<path>`/`<text>` 落入 **HTML 命名空间、位于已闭合的 `<svg>` 之外**，SVG 从不渲染这些节点——数据线不可见（`<path/>` 自闭合被当成空的内联元素），横轴刻度则退化为紧跟 `<svg>` 之后的普通内联文本、堆在左边缘。现改为**先把网格、曲线、刻度拼成完整 SVG 字符串，再一次性赋值给 `innerHTML`**（与同页 sparkline / 主时间序列图一致）。
  - 用真实库（5000 个快照，`paid_balance` 20.7→217.3）在 headless Chromium 中复现并验证：修复前 `svg path` 节点数为 0、曲线缺失；修复后 `d` 长度 176874、`stroke` 解析为 `rgb(22,163,74)`、`getTotalLength()` ≈ 1193.6，曲线与正确的横轴刻度均正常渲染。

### Tests
- 新增 `dashboard/colab_history_chart.test.js`：加载真实 `template.html` 的 `renderColabHistoryChart`，断言序列 `<path>` 与坐标 `<text>` 都位于唯一 `<svg>` 内部、且函数不再使用 `innerHTML +=`（防止回归到「有轴无图」）；并覆盖快照不足两点时提示「数据将随时间累积」。

## v0.11.23 (2026-10-08)

### Fixes
- **`models_without_price` 误报（0 token 的失败请求）**: 健康页报「2 model(s) in usage have no price」，查证为 `kiro-claude-opus-4-5` / `kiro-claude-sonnet-4-5` —— 来自新 `kiro` provider 的**全部失败（HTTP 400）、0 token** 探测请求。0 token 的请求无论是否定价成本都是 $0，因此把「出现在用量里」当作「缺定价」属于误报。
  - `countUnpricedModels` 改为**只统计真正消耗过 token 的模型**（`HAVING SUM(total_tokens)>0 OR SUM(input_tokens)>0 OR SUM(output_tokens)>0`），单次失败探测不会再造成永久告警；
  - 告警文案同步改为「N model(s) **with token usage** have no price」。
- **客户端前缀模型名无法定价（未来会真实少算）**: 价格表只有 `claude-opus-4-5` / `claude-sonnet-4-5`，没有 `kiro-` 变体；若 kiro 之后返回带 token 的真实流量，`kiro-claude-opus-4-5` 会被记为 $0。现在 `matchPriceDetailedWithPreview` 增加**逐级剥离前导分段**的回退（`kiro-claude-opus-4-5` → `claude-opus-4-5`），可泛化覆盖任何未见过的前缀（如 cursor-），且因为必须精确命中已存在的价格键，匹配范围有界；精确匹配仍然优先。

### Tests
- `TestMatchPriceResolvesClientPrefixedModels`（前缀回退 + 精确匹配优先 + 未知模型不得凭空匹配）。
- `TestCountUnpricedModelsIgnoresZeroTokenModels`（0 token 模型不计入告警）。

实机验证：真实库未定价模型数由 2 → **0**（`kiro-*` 均解析到对应 Claude 价格）。

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
