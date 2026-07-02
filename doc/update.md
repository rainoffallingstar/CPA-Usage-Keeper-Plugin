# Hot-reload Deployment Guide

## Current Version

**v0.7.0** — Quota cards with unified add-account bar, reset-time display, toast notifications, SVG icons, event drawer, search, responsive.

## Next Expected Version

**v0.7.0**

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

### Step 3: Deploy new dylib

```bash
cp dist/usage-keeper.dylib \
  "/Users/fallingstar/Library/Application Support/Quotio/proxy/upstream/v7.2.42/plugins/darwin/arm64/usage-keeper-vX.Y.Z.dylib"
```

### Step 4: Toggle config to trigger hot-reload

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

### Step 5: Verify

```bash
curl -s -o /dev/null -w "%{http_code}" "http://localhost:18317/v0/resource/plugins/usage-keeper/dashboard"
# Expected: 200

curl -s "http://localhost:18317/v0/resource/plugins/usage-keeper/dashboard" | grep -c '%!d'
# Expected: 0
```

---

## Critical Rules

1. **Never delete `usage-keeper.db`** — data is irrecoverable. Database path:
   ```
   ~/Library/Application Support/Quotio/proxy/upstream/v7.2.42/data/usage-keeper.db
   ```

2. **Never use `rm -f` on the DB file** during deployment. This was done during early debugging but left in deploy scripts by mistake — it wiped historical data on every update.

3. **Always delete old dylibs before deploying new one.** CPA picks the highest versioned filename. Stale dylibs with the same or higher version string can shadow the new build.

4. **After config toggle, verify `%!d` count is 0.** Non-zero means Go's `fmt.Sprintf` found a single `%` in the embedded template that should be `%%`. Check CSS for `width: 100%` → must be `width: 100%%`.

5. **Never cold-start CPA with plugin present.** The `db.Query` + `rows.Scan` calls in `loadRecentIntoRing()`, `loadOpenCodeAccountsFromDB()`, `loadGlmAccountsFromDB()`, `loadDeepseekAccountsFromDB()`, and `loadPricesFromDB()` all trigger SIGSEGV in the CGO context during CPA boot. These are deferred to `lazyInit()` which fires on the first API request after the Go runtime has stabilized.

---

## Plugin Directory

```
current/ → v7.2.42/   (symlink)
v7.2.42/
  CLIProxyAPI         (CPA binary)
  plugins/darwin/arm64/
    usage-keeper-vX.Y.Z.dylib   ← deploy here
  data/
    usage-keeper.db   ← DO NOT DELETE
```

---

## Version History

| Version | Changes |
|---------|---------|
| v0.6.4 | Reset-time display in quota progress bars (left/right aligned) |
| v0.6.3 | `!!%%` fix in CSS, add-account unified bar |
| v0.6.2 | Ternary comma operator fix in JS |
| v0.6.1 | SVG icon system, toast, drawer, search, design tokens |
| v0.6.0 | DeepSeek balance monitoring, dashboard visual overhaul |
| v0.5.x | GLM persistence, lazy init, race-condition fixes |
| v0.4.9 | OpenCode Go quota + GLM Coding Plan |
