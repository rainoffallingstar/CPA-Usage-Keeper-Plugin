# cpa-plugin-usage-keeper

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that collects, stores, and visualizes API usage data inside the CPA process -- no separate server, no external database, no polling.

- Receives usage records from CPA in real time via the UsagePlugin callback
- Stores every request in an embedded SQLite database
- Exposes a browser dashboard with summary cards, model breakdowns, and event history
- Provides a Quotio-compatible GET /v0/management/usage endpoint
- Single file -- one main.go, one shared library binary, one config.yaml section

## Prerequisites

- CLIProxyAPI v7.1.74 or later
- Go 1.26+ (only needed to build)

## Quick start

```
cd cpa-plugin-usage-keeper
make build
mkdir -p /path/to/cpa-working-dir/plugins/darwin/arm64
cp dist/usage-keeper.dylib /path/to/cpa-working-dir/plugins/darwin/arm64/
# Add config block to CPA config.yaml (see below)
# Touch config.yaml to hot-reload, or restart CPA
```

## Build

```
make build
```

| Platform | Binary |
|---|---|
| macOS | dist/usage-keeper.dylib |
| Linux / FreeBSD | dist/usage-keeper.so |
| Windows | dist/usage-keeper.dll |

Cross-compile:

```
GOOS=linux GOARCH=amd64 make build
```

## Configuration

Add this block to your CPA config.yaml:

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
```

**Configuration keys**

| Key | Type | Default | Description |
|---|---|---|---|
| db_path | string | usage-keeper.db | SQLite file path, relative to CPA working directory |
| retention_days | integer | 90 | Auto-delete records older than this many days |
| max_in_memory_events | integer | 1000 | Ring buffer size for instant dashboard rendering (max 10000) |
| refresh_seconds | integer | 0 | Dashboard auto-refresh interval. 0 = off. Range: 0-3600 |

See config.example.yaml for the annotated version.

## Endpoints

### Browser dashboard (no authentication)

| Path | Description |
|---|---|
| /v0/resource/plugins/usage-keeper/dashboard | Interactive usage dashboard with summary cards, model breakdowns, event history, time-range selector, theme toggle |

### Resource API (no authentication)

All paths under /v0/resource/plugins/usage-keeper/api/.

| Path | Params | Description |
|---|---|---|
| /api/summary | range (1h/6h/24h/7d/30d) | Aggregate token and request counts |
| /api/models | range | Token and request breakdown per model |
| /api/events | range, limit, offset | Paginated usage event list |
| /api/usage | -- | Quotio-compatible aggregate |

### Management API (requires management key)

| Path | Method | Description |
|---|---|---|
| /v0/management/usage | GET | Quotio-compatible aggregate (Quotio queries this exact path) |
| /v0/management/usage-keeper/summary | GET | Aggregate usage stats |
| /v0/management/usage-keeper/models | GET | Per-model breakdown |
| /v0/management/usage-keeper/events | GET | Paginated event list |
| /v0/management/usage-keeper/cleanup | POST | Manually trigger retention cleanup |

## Quotio integration

Quotio calls GET /v0/management/usage for aggregate usage statistics. This plugin registers that exact path and returns data in the format Quotio expects:

```json
{
  "usage": {
    "total_requests": 135,
    "success_count": 135,
    "failure_count": 0,
    "total_tokens": 21550203,
    "input_tokens": 21502129,
    "output_tokens": 48074
  },
  "failed_requests": 0
}
```

No changes to Quotio source code are needed.

## Upgrading

1. Build with a bumped version:

   VERSION=0.4.0 make build

2. Copy with a versioned filename -- CPA hot-reloads without restarting:

   cp dist/usage-keeper.dylib plugins/darwin/arm64/usage-keeper-v0.4.0.dylib
   rm plugins/darwin/arm64/usage-keeper-v0.3.3.dylib

## Troubleshooting

### Dashboard returns 404

- Ensure plugins.enabled: true in your CPA config.yaml.
- Check the binary is in the correct plugins/GOOS/GOARCH/ subdirectory.
- Restart CPA or touch config.yaml to trigger a hot-reload.

### No usage data appears

- Data only accumulates for requests that happen after the plugin loads.
- The /usage endpoint aggregates the last 24 hours by default.

### Plugin fails to load

- Check CPA logs for lines containing pluginhost:.
- Verify the binary matches CPA's OS and architecture: file dist/usage-keeper.dylib.
- The file must be a shared library, not a standalone executable.

### Quotio still shows no usage data

- Confirm the plugin is loaded: visit /v0/resource/plugins/usage-keeper/dashboard.
- Verify Quotio's management key matches CPA's remote-management.secret-key.
- Test directly:

  curl -H "Authorization: Bearer <your-key>" http://localhost:18317/v0/management/usage

## Acknowledgments

This plugin is inspired by the standalone [cpa-usage-keeper](https://github.com/Willxup/cpa-usage-keeper) project by [@Willxup](https://github.com/Willxup).

## Community

Learn AI at L Forum.

[LinuxDO](https://linux.do/)
