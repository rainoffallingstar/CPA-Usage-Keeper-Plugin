# Hot-reload Deployment Guide

## Current Version

**v0.8.0** — Capsule segmented-control tabs, global search on tab row, DeepSeek ring progress with tiered total.

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

1. **Never delete `usage-keeper.db`** — data is irrecoverable. Database path:
   ```
   ~/Library/Application Support/Quotio/proxy/upstream/v7.2.42/data/usage-keeper.db
   ```

2. **Never use `rm -f` on the DB file** during deployment.

3. **Always delete old dylibs before deploying new one.** CPA picks the highest versioned filename. Stale dylibs can shadow the new build.

4. **Always bump the version number for hot-reload.** CPA will not reload a plugin whose filename version is ≤ the currently loaded version. Overwriting the same filename after CPA has already loaded it will NOT trigger a reload.

5. **After config toggle, verify `%!d` count is 0.** Non-zero means Go's `fmt.Sprintf` found a single `%` in the embedded template that should be `%%`. Check CSS for `width: 100%` → must be `width: 100%%`.

6. **Never cold-start CPA with plugin present.** The `db.Query` + `rows.Scan` calls trigger SIGSEGV in the CGO context during CPA boot. They are deferred to `lazyInit()` which fires on the first API request.

---

## Plugin Directory

```
current/ → v7.2.42/   (symlink)
v7.2.42/
  CLIProxyAPI         (CPA binary)
  plugins/darwin/arm64/
    usage-keeper-vX.Y.Z.dylib   ← deploy here (must be highest version)
  data/
    usage-keeper.db   ← DO NOT DELETE
```

---

## Version History

| Version | Changes |
|---------|---------|
| v0.8.0 | Capsule segmented-control tabs, global search on tab row, DeepSeek ring progress with tiered total |
| v0.7.0 | Remove provider filter, unified search box with dynamic placeholder, DeepSeek tiered ring |
| v0.6.4 | Reset-time display in quota progress bars (left/right aligned) |
| v0.6.3 | `!!%%` fix in CSS, add-account unified bar |
| v0.6.2 | Ternary comma operator fix in JS |
| v0.6.1 | SVG icon system, toast, drawer, search, design tokens |
| v0.6.0 | DeepSeek balance monitoring, dashboard visual overhaul |
| v0.5.x | GLM persistence, lazy init, race-condition fixes |
| v0.4.9 | OpenCode Go quota + GLM Coding Plan |
