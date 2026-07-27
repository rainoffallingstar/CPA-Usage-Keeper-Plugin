# Hot-reload Deployment Guide

## Current Version

**v0.10.12** — Manual refresh on By Model/All Events tabs, fuzzy model aggregation with Detailed/Aggregated view toggle, total cost summary.

## ⚠️ Before Building — Version Checklist

**Hot-reload requires the dylib filename version to be higher than any previously deployed dylib.** CPA picks the highest versioned filename in the plugins directory. If you reuse the same version number, CPA will NOT reload the plugin.

Before every build, complete these 3 steps in order:

### 1. Check current version in source

```bash
grep 'pluginVersion' types.go
# Expected: var pluginVersion = "0.X.Y"
```

### 2. Bump version in `types.go`

```bash
# Edit types.go: var pluginVersion = "0.X.Y" → "0.X.Z"
```

### 3. Update this document

```bash
# Update "Current Version" above and add entry to Version History below
```

---

## Deployment Flow

CPA cannot cold-start with the plugin present (CGO + `modernc.org/sqlite` SIGSEGV during `db.Query`). Always deploy via hot-reload:

### Quick path: use the migration script (recommended)

When CPA auto-upgrades (e.g. `v7.2.42` → `v7.2.88`) and the plugin says "未注册", run from the repo root:

```bash
# 1. Dry-run first — reports every operation without changing files
./scripts/migrate-plugin.sh

# 2. Migrate the latest already-deployed dylib and database
./scripts/migrate-plugin.sh --apply

# Or build the version declared in types.go before migrating
./scripts/migrate-plugin.sh --apply --force-build
```

The script automatically:
- Resolves `current` to the active CPA version directory
- Selects the highest versioned `usage-keeper-v*.dylib`, or builds the source version with `--force-build`
- Deploys the dylib into `plugins/<platform>/`
- Moves or copies the freshest database to the version-independent `upstream/data/usage-keeper.db`
- Links the active version's `data/usage-keeper.db` to that canonical database
- Preserves a real version-local database as a timestamped backup if a canonical database already exists
- Toggles `refresh_seconds` in `config.yaml` to trigger hot-reload when CPA is running
- Verifies that the dashboard returns HTTP 200

After the first successful migration, future CPA upgrades keep using the same canonical database; only the dylib needs copying into the new version directory.

Environment overrides (optional):

```bash
CPA_UPSTREAM_DIR="..."       # default: ~/Library/Application Support/Quotio/proxy/upstream
CPA_CONFIG_FILE="..."        # default: ~/Library/Application Support/Quotio/config.yaml
CPA_CANONICAL_DB_DIR="..."   # default: $CPA_UPSTREAM_DIR/data
PLUGIN_SOURCE_DIR="..."      # default: repository root
```

### Manual path: full cold-start redeploy

Use this only if the quick path fails or for a fresh build.

### Step 1: Kill CPA, clean old dylibs

```bash
pkill -9 -f CLIProxyAPI
sleep 3
find "/Users/fallingstar/Library/Application Support/Quotio/proxy/upstream/" -name "*.dylib" -delete
```

### Step 2: Start CPA without plugin

```bash
"/Users/fallingstar/Library/Application Support/Quotio/proxy/upstream/current/CLIProxyAPI" \
  -config "/Users/fallingstar/Library/Application Support/Quotio/config.yaml" > /tmp/cpa.log 2>&1 &
sleep 12
```

### Step 3: Build with correct version

```bash
# The VERSION here MUST match types.go and the dylib filename below.
# All three must be the same value.
make build VERSION=0.8.0
```

### Step 4: Deploy new dylib

```bash
# The version in the filename MUST be higher than any previously deployed dylib.
# CPA compares filenames lexicographically to pick the latest plugin.
cp dist/usage-keeper.dylib \
  "/Users/fallingstar/Library/Application Support/Quotio/proxy/upstream/v7.2.42/plugins/darwin/arm64/usage-keeper-v0.8.0.dylib"
```

### Step 5: Toggle config to trigger hot-reload

```bash
python3 -c "
c = open('/Users/fallingstar/Library/Application Support/Quotio/config.yaml').read()
open('/Users/fallingstar/Library/Application Support/Quotio/config.yaml', 'w').write(
    c.replace('refresh_seconds: 0', 'refresh_seconds: 10')
)
"
sleep 8
python3 -c "
c = open('/Users/fallingstar/Library/Application Support/Quotio/config.yaml').read()
open('/Users/fallingstar/Library/Application Support/Quotio/config.yaml', 'w').write(
    c.replace('refresh_seconds: 10', 'refresh_seconds: 0')
)
"
sleep 5
```

### Step 6: Verify

```bash
curl -s -o /dev/null -w "%{http_code}" "http://localhost:18317/v0/resource/plugins/usage-keeper/dashboard"
# Expected: 200

curl -s "http://localhost:18317/v0/resource/plugins/usage-keeper/dashboard" | grep -c '%!d'
# Expected: 0
```

---

## Version Sync Checklist

Every deploy must keep these three values identical:

| Location | Value | How to update |
|---|---|---|
| `types.go` | `var pluginVersion = "0.X.Y"` | Edit directly |
| `Makefile` build | `make build VERSION=0.X.Y` | Pass as CLI arg |
| Dylib filename | `usage-keeper-v0.X.Y.dylib` | Rename during cp |

**If any of these three don't match, the deploy will silently fail or serve stale code.**

---

## Critical Rules

1. **Never delete `usage-keeper.db`** — data is irrecoverable. After migration, the canonical database is:
   ```
   ~/Library/Application Support/Quotio/proxy/upstream/data/usage-keeper.db
   ```
   Each active version's `data/usage-keeper.db` is a symlink to this file.

2. **Never use `rm -f` on the database or its backups** during deployment.

3. **Keep only intentional dylib versions in the active plugin directory.** CPA selects the highest versioned filename; an accidentally higher stale filename can shadow the intended build.

4. **Always bump the version number for hot-reload.** CPA will not reload a plugin whose filename version is ≤ the currently loaded version. Overwriting the same filename after CPA has already loaded it will NOT trigger a reload.

5. **After config toggle, verify `%!d` count is 0.** Non-zero means Go's `fmt.Sprintf` found a single `%` in the embedded template that should be `%%`. Check CSS for `width: 100%` → must be `width: 100%%`.

6. **Never cold-start CPA with plugin present.** The `db.Query` + `rows.Scan` calls trigger SIGSEGV in the CGO context during CPA boot. They are deferred to `lazyInit()` which fires on the first API request.

---

## Plugin Directory

```
upstream/
  current → v7.2.88/                  # active CPA version
  data/
    usage-keeper.db                   # canonical database; never delete
  v7.2.42/
    plugins/darwin/arm64/
      usage-keeper-v0.10.10.dylib     # reusable migration source
    data/
      usage-keeper.db                 # old version-local database, if any
  v7.2.88/
    CLIProxyAPI
    plugins/darwin/arm64/
      usage-keeper-v0.10.10.dylib     # active deployed plugin
    data/
      usage-keeper.db → ../../../data/usage-keeper.db
```

---

## Version History

| Version | Changes |
|---------|---------|
| v0.10.12 | Manual refresh on By Model/All Events tabs, fuzzy model aggregation with Detailed/Aggregated view toggle, total cost summary |
| v0.10.11 | apple-design dashboard refresh: interruptible drag-to-dismiss drawer with velocity handoff + momentum projection, translucent tab/drawer chrome (`backdrop-filter`), spring + symmetric easing tokens, size-specific typography (`font-optical-sizing`, `--tracking-*`), `:active` instant press feedback, `prefers-reduced-motion`/`prefers-reduced-transparency`/`prefers-contrast` support |
| v0.10.10 | Tiered DeepSeek pricing, consistent time formatting, quota provider documentation |
| v0.8.3 | Full UI redesign: Slate palette, gradient area sparklines, dual-color stacked token bars, cost heatmap, executor brand tags, expandable error rows, provider-grouped pricing, health breathing light, tri-stage quota progress bars |
| v0.8.2 | Fix pricing tab blank: auto-sync now persists to SQLite (survives restart), manually-added prices protected from auto-sync overwrite, DB load no longer races with auto-sync goroutine |
| v0.8.1 | Default 30-day range for all endpoints, composite `(timestamp, id DESC)` index, capped COUNT(*) at 10K, SQLite 3-conn pool, server response cache (2s TTL), ETag caching (304 Not Modified), frontend ETag support |
| v0.8.0 | Capsule segmented-control tabs, global search on tab row, DeepSeek ring progress with tiered total |
| v0.7.0 | Remove provider filter, unified search box with dynamic placeholder, DeepSeek tiered ring |
| v0.6.4 | Reset-time display in quota progress bars (left/right aligned) |
| v0.6.3 | `!!%%` fix in CSS, add-account unified bar |
| v0.6.2 | Ternary comma operator fix in JS |
| v0.6.1 | SVG icon system, toast, drawer, search, design tokens |
| v0.6.0 | DeepSeek balance monitoring, dashboard visual overhaul |
| v0.5.x | GLM persistence, lazy init, race-condition fixes |
| v0.4.9 | OpenCode Go quota + GLM Coding Plan |
