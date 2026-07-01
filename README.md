# Usage Keeper — CPA Plugin

AI API 用量统计与费用核算插件，运行在 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 进程内。

## 功能

- 实时采集用量数据（UsagePlugin 回调 -> SQLite）
- 浏览器 Dashboard：摘要卡片、模型拆分、事件历史、暗色主题、时间范围筛选
- 模型定价：手动添加 或 自动从 modelprice.boxtech.icu 同步 650+ 模型
- OpenCode Go 套餐余额监控（支持工作区切换）
- 健康监控：缓存命中率、写入延迟、环缓冲使用率、告警
- 导入/导出、后台导出任务管理（JSON/JSONL/CSV+gzip）
- API Key 脱敏（SHA-224 哈希 + 显示掩码）
- Quotio 兼容的 GET /v0/management/usage 端点
- 5 平台交叉编译（GitHub Actions）

## 快速开始

```bash
make build
cp dist/usage-keeper.dylib 你的CPA目录/plugins/darwin/arm64/
# 在 CPA config.yaml 中添加配置块
# 重启 CPA 或 touch config.yaml 触发热重载
```

## Dashboard

打开 `http://你的CPA地址:端口/v0/resource/plugins/usage-keeper/dashboard`

| 标签页 | 说明 |
|--------|------|
| By Model | 按模型拆分的用量表（Token、请求数、Cost） + Provider 过滤 |
| All Events | 分页事件列表（时间、模型、来源、Token、缓存命中率、延迟） |
| Pricing | 添加/删除/自动同步模型定价 |
| Health | 运行状态、环缓冲、缓存命中率、存储信息、告警 |
| Quota | OpenCode Go 套餐余额（5小时滚动/本周/本月）+ 工作区切换 |

## 配置

```yaml
plugins:
  enabled: true
  dir: ./plugins
  configs:
    usage-keeper:
      enabled: true
      priority: 1
      db_path: ./data/usage-keeper.db
      retention_days: 90
      max_in_memory_events: 1000
      refresh_seconds: 0
      # 可选：在 config 中预定义 OpenCode 账号
      opencode_go_accounts:
        - name: "我的 Go 套餐"
          auth_cookie: ""
          workspace_id: ""
```

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| db_path | string | usage-keeper.db | SQLite 文件路径 |
| retention_days | integer | 90 | 数据保留天数 |
| max_in_memory_events | integer | 1000 | Dashboard 环缓冲大小（最大 10000） |
| refresh_seconds | integer | 0 | 自动刷新间隔（0 = 关闭, ≤3600） |
| opencode_go_accounts | list | [] | OpenCode Go 账号预定义（也可在 Dashboard 中直接添加） |

## API 端点

所有路径前缀 `/v0/resource/plugins/usage-keeper`

### 资源 API（无需认证）

| 路径 | 参数 | 说明 |
|------|------|------|
| `/dashboard` | — | Dashboard HTML |
| `/api/summary` | `range` | 聚合统计（Token/请求/缓存命中率） |
| `/api/models` | `range`, `provider` | 按模型拆分 |
| `/api/events` | `range`, `limit`, `offset` | 分页事件列表 |
| `/api/health` | — | 健康监控 |
| `/api/prices` | — | 模型定价 |
| `/api/prices/sync` | — | 触发从 modelprice.boxtech.icu 同步定价 |
| `/api/opencode-quota` | — | OpenCode Go 套餐配额 |

### Management API（需要管理密钥）

所有路径前缀 `/v0/management/usage-keeper`

| 路径 | 方法 | 说明 |
|------|------|------|
| `/summary` | GET | 聚合统计 |
| `/models` | GET | 按模型拆分 |
| `/events` | GET | 分页事件 |
| `/cleanup` | POST | 手动清理过期数据 |
| `/health` | GET | 健康监控 |
| `/prices` | GET/PUT/DELETE/POST | 模型定价 CRUD |
| `/export` | GET | 导出数据 |
| `/import` | POST | 导入数据 |
| `/export-jobs` | GET/POST/DELETE | 导出任务管理 |
| `/export-download` | GET | 下载导出文件 |
| `/opencode-quota` | GET/POST | OpenCode Go 配额 |

兼容端点：`/v0/management/usage` (Quotio 聚合)

## 模型定价

### 自动同步

插件启动后自动从 [modelprice.boxtech.icu](https://modelprice.boxtech.icu) 拉取 650+ 模型的定价（每 6 小时刷新一次）。

Pricing 标签页中点击 **Sync** 按钮可手动触发。同步后的定价支持模糊匹配（`glm-5-2` / `glm-5.2` / `deepseek.v4.pro` 等写法均可）。

### 手动管理

在 Pricing 标签页中直接添加/删除模型定价，价格单位为 **美元/百万 Token**。

## OpenCode Go 余额监控

1. 打开 Dashboard，切换到 **Quota** 标签页
2. 点击 **Add Account**，输入账号名
3. 点击 **Set Cookie**，粘贴浏览器中的 `auth=xxx` cookie
4. 保存后自动拉取 5 小时滚动 / 本周 / 本月用量百分比
5. 支持多工作区切换（自动解析或手动指定 `wrk_xxx`）

已保存的账号存储在 SQLite 中，重启 CPA 后自动恢复。

## 构建

```bash
make build                    # 当前平台
GOOS=linux GOARCH=amd64 make build  # 交叉编译
```

| 平台 | 产物 |
|------|------|
| macOS arm64 / amd64 | dist/usage-keeper.dylib |
| Linux amd64 / arm64 | dist/usage-keeper.so |
| Windows amd64 | dist/usage-keeper.dll |

## GitHub Actions

推送到 `main` 分支自动构建测试。推送 `v*` tag 触发 Release（包含 5 平台二进制 + checksums）。

```bash
git tag v$(date +%Y%m%d)
git push origin v$(date +%Y%m%d)
```

## 升级

```bash
make build VERSION=0.5.0
cp dist/usage-keeper.dylib plugins/darwin/arm64/usage-keeper-v0.5.0.dylib
rm plugins/darwin/arm64/usage-keeper-v0.4.9.dylib
```

版本化文件名让 CPA 在热重载时自动加载最新版本。

## 故障排除

| 问题 | 排查 |
|------|------|
| Dashboard 404 | 确认 `plugins.enabled: true`，检查文件在正确的 `plugins/GOOS/GOARCH/` 目录下，重启 CPA |
| 无用量数据 | 只有插件加载后的请求才会被统计，`/usage` 端点默认聚合最近 24 小时 |
| 插件加载失败 | 检查 CPA 日志中 `pluginhost:` 行，`file` 确认架构匹配 |
| Quotio 无数据 | 访问 Dashboard 确认插件已加载，检查管理密钥配置 |

## 致谢

受 [cpa-usage-keeper](https://github.com/Willxup/cpa-usage-keeper) by [@Willxup](https://github.com/Willxup) 启发。
