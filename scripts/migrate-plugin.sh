#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# migrate-plugin.sh — Post-CPA-upgrade plugin migration
#
# When CPA auto-upgrades (e.g. v7.2.42 → v7.2.88), the new version directory
# has no usage-keeper dylib or database, so the plugin shows "未注册".
# This script fixes that in one shot:
#
#   1. Copies the latest usage-keeper dylib into the new version's plugins dir
#   2. Relocates the SQLite DB to a version-independent canonical location
#      (proxy/upstream/data/usage-keeper.db) and symlinks each version's
#      data/usage-keeper.db to it — so future upgrades never lose data
#   3. Toggles refresh_seconds in config.yaml to trigger CPA hot-reload
#   4. Verifies the dashboard returns 200
#
# The DB symlink means config.yaml's relative "db_path: ./data/usage-keeper.db"
# stays valid across upgrades — no config edit needed.
#
# Usage:
#   ./scripts/migrate-plugin.sh --apply                # perform migration
#   ./scripts/migrate-plugin.sh --apply --force-build  # build source first
#   ./scripts/migrate-plugin.sh --apply -v             # show debug lines
#
# Environment overrides:
#   CPA_UPSTREAM_DIR        default: ~/Library/Application Support/Quotio/proxy/upstream
#   CPA_CONFIG_FILE         default: ~/Library/Application Support/Quotio/config.yaml
#   CPA_CANONICAL_DB_DIR    default: $CPA_UPSTREAM_DIR/data
#   HOT_RELOAD_*_WAIT_SECONDS  test/tuning overrides for reload delays
# ---------------------------------------------------------------------------
set -euo pipefail

# ── Configurable paths ──────────────────────────────────────────────────────
UPSTREAM_DIR="${CPA_UPSTREAM_DIR:-$HOME/Library/Application Support/Quotio/proxy/upstream}"
CONFIG_FILE="${CPA_CONFIG_FILE:-$HOME/Library/Application Support/Quotio/config.yaml}"
PLUGIN_NAME="${PLUGIN_NAME:-usage-keeper}"
PLUGIN_SOURCE_DIR="${PLUGIN_SOURCE_DIR:-$(cd "$(dirname "$0")/.." && pwd)}"
HOT_RELOAD_FIRST_WAIT_SECONDS="${HOT_RELOAD_FIRST_WAIT_SECONDS:-8}"
HOT_RELOAD_SECOND_WAIT_SECONDS="${HOT_RELOAD_SECOND_WAIT_SECONDS:-5}"
HOT_RELOAD_VERIFY_WAIT_SECONDS="${HOT_RELOAD_VERIFY_WAIT_SECONDS:-3}"
LOG_PREFIX="[migrate-plugin]"

# Canonical DB lives OUTSIDE any version directory, so it survives CPA upgrades.
# Each version's data/usage-keeper.db becomes a symlink pointing here.
CANONICAL_DB_DIR="${CPA_CANONICAL_DB_DIR:-$UPSTREAM_DIR/data}"

# ── Flags ───────────────────────────────────────────────────────────────────
DRY_RUN=true
FORCE_BUILD=false
VERBOSE=0

for arg in "$@"; do
    case "$arg" in
        --apply)       DRY_RUN=false ;;
        --force-build) FORCE_BUILD=true ;;
        --verbose|-v)  VERBOSE=1 ;;
        --help|-h)
            sed -n '2,/^$/p' "$0"
            exit 0
            ;;
        *)
            echo "Unknown argument: $arg"
            echo "Usage: $0 [--apply] [--force-build] [--verbose]"
            exit 1
            ;;
    esac
done

# ── Logging ─────────────────────────────────────────────────────────────────
log()  { printf '%s %s\n' "$LOG_PREFIX" "$*"; }
warn() { printf '%s [WARN] %s\n' "$LOG_PREFIX" "$*" >&2; }
err()  { printf '%s [ERROR] %s\n' "$LOG_PREFIX" "$*" >&2; }
vlog() { [ "$VERBOSE" = "1" ] && printf '%s [DEBUG] %s\n' "$LOG_PREFIX" "$*" || true; }

# ── Platform detection ──────────────────────────────────────────────────────
detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m | tr '[:upper:]' '[:lower:]')"
    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
    esac
    printf '%s/%s' "$os" "$arch"
}

PLATFORM="$(detect_platform)"
DYLIB_EXT="dylib"
[ "$(uname -s)" = "Linux" ] && DYLIB_EXT="so"
[ "$(uname -s)" = "Windows" ] && DYLIB_EXT="dll"

# ── Helper functions ────────────────────────────────────────────────────────

# Portable version comparison (BSD sort on macOS lacks -V).
# Returns 0 (true) if $1 > $2, else 1 (false).
version_gt() {
    awk -v a="$1" -v b="$2" '
    BEGIN {
        na = split(a, aa, ".")
        nb = split(b, bb, ".")
        n = (na > nb) ? na : nb
        for (i = 1; i <= n; i++) {
            av = (i <= na) ? aa[i] + 0 : 0
            bv = (i <= nb) ? bb[i] + 0 : 0
            if (av > bv) exit 0
            if (av < bv) exit 1
        }
        exit 1
    }'
}

# Find the CPA version directory that `current` points to.
# Returns the full path, e.g. .../upstream/v7.2.88
get_current_version_dir() {
    local link="$UPSTREAM_DIR/current"
    if [ ! -L "$link" ]; then
        err "current symlink not found at $link"
        exit 1
    fi
    local target
    target="$(readlink "$link")"
    # If relative, resolve against upstream dir
    case "$target" in
        /*) printf '%s' "$target" ;;
        *)  printf '%s/%s' "$UPSTREAM_DIR" "$target" ;;
    esac
}

# Find all CPA version directories (e.g. v7.2.42, v7.2.88) sorted descending.
get_all_version_dirs() {
    find "$UPSTREAM_DIR" -maxdepth 1 -type d -name 'v*' \
        | sort -t. -k1,1r -k2,2r -k3,3r
}

# Find the latest usage-keeper dylib across ALL old version directories.
# Returns: path to dylib file
find_latest_dylib() {
    local latest=""
    local latest_ver=""
    while IFS= read -r dir; do
        local found=""
        while IFS= read -r candidate; do
            [ -z "$candidate" ] && continue
            local cand_ver
            cand_ver="$(extract_dylib_version "$(basename "$candidate")")"
            if [ -z "$found" ] || version_gt "$cand_ver" "$(extract_dylib_version "$(basename "$found")")"; then
                found="$candidate"
            fi
        done < <(find "$dir/plugins/$PLATFORM" -maxdepth 1 \
            -name "${PLUGIN_NAME}-v*.${DYLIB_EXT}" 2>/dev/null) || true
        if [ -n "$found" ] && [ -f "$found" ]; then
            local ver
            ver="$(extract_dylib_version "$(basename "$found")")"
            if [ -z "$latest" ] || version_gt "$ver" "$latest_ver"; then
                latest="$found"
                latest_ver="$ver"
            fi
        fi
    done <<< "$(get_all_version_dirs)"
    printf '%s' "$latest"
}

# Find the most recently modified usage-keeper.db across version directories.
find_latest_db() {
    local latest=""
    while IFS= read -r dir; do
        local db="$dir/data/usage-keeper.db"
        if [ -f "$db" ] && { [ -z "$latest" ] || [ "$db" -nt "$latest" ]; }; then
            latest="$db"
        fi
    done <<< "$(get_all_version_dirs)"
    printf '%s' "$latest"
}

# Extract version from dylib filename: usage-keeper-v0.10.10.dylib → 0.10.10
extract_dylib_version() {
    echo "$1" | sed 's/.*-v\([0-9.]*\)\.'"${DYLIB_EXT}"'/\1/'
}

# Read the plugin version declared by the source tree.
get_source_version() {
    local source_version
    source_version="$(sed -n 's/^[[:space:]]*var pluginVersion = "\([^"]*\)".*/\1/p' \
        "$PLUGIN_SOURCE_DIR/types.go" | head -n1)"
    if [ -z "$source_version" ]; then
        err "Could not read pluginVersion from $PLUGIN_SOURCE_DIR/types.go"
        exit 1
    fi
    printf '%s' "$source_version"
}

# Test if CPA is running on its known port.
is_cpa_running() {
    pgrep -q -f CLIProxyAPI 2>/dev/null
}

# Read a YAML value from config (simple sed-based, handles indentation).
yaml_value() {
    sed -n "s/^[[:space:]]*$1:[[:space:]]*//p" "$CONFIG_FILE" 2>/dev/null \
        | head -n1 \
        | sed 's/[[:space:]]*#.*$//' \
        | sed 's/^[[:space:]]*//;s/[[:space:]]*$//;s/^"//;s/"$//'
}

# Replace one refresh_seconds value while preserving indentation and comments.
set_refresh_seconds() {
    local expected_value="$1"
    local replacement_value="$2"

    python3 - "$CONFIG_FILE" "$expected_value" "$replacement_value" <<'PY'
import pathlib
import re
import sys

config_path = pathlib.Path(sys.argv[1])
expected_value = sys.argv[2]
replacement_value = sys.argv[3]
config_text = config_path.read_text()
pattern = re.compile(
    r"^(\s*refresh_seconds:\s*)" + re.escape(expected_value) + r"(\s*(?:#.*)?)$",
    re.MULTILINE,
)
updated_text, replacement_count = pattern.subn(
    lambda match: f"{match.group(1)}{replacement_value}{match.group(2)}",
    config_text,
)
if replacement_count != 1:
    raise SystemExit(
        f"expected one refresh_seconds: {expected_value} entry, found {replacement_count}"
    )
config_path.write_text(updated_text)
PY
}

# Toggle refresh_seconds in config.yaml to trigger CPA hot-reload.
trigger_hot_reload() {
    local current_val toggled_val
    if [ ! -f "$CONFIG_FILE" ]; then
        err "CPA config file not found: $CONFIG_FILE"
        return 1
    fi

    current_val="$(yaml_value refresh_seconds)"
    if [ -z "$current_val" ]; then
        err "refresh_seconds is missing from $CONFIG_FILE; cannot trigger hot-reload safely."
        return 1
    fi

    if [ "$current_val" = "0" ]; then
        toggled_val="10"
    else
        toggled_val="0"
    fi

    log "Triggering hot-reload: refresh_seconds $current_val → $toggled_val → $current_val"
    set_refresh_seconds "$current_val" "$toggled_val" || {
        err "Failed to write the first config toggle."
        return 1
    }

    sleep "$HOT_RELOAD_FIRST_WAIT_SECONDS"

    set_refresh_seconds "$toggled_val" "$current_val" || {
        err "Failed to restore refresh_seconds to $current_val."
        return 1
    }

    sleep "$HOT_RELOAD_SECOND_WAIT_SECONDS"
    log "Hot-reload toggle complete"
}

# Verify the plugin is serving requests.
verify_plugin() {
    local code
    code="$(curl -s -o /dev/null -w '%{http_code}' \
        'http://localhost:18317/v0/resource/plugins/usage-keeper/dashboard' 2>/dev/null)" || code="000"
    [ "$code" = "200" ]
}

# Ensure the DB is symlinked from the version dir to a version-independent
# canonical location, so CPA upgrades never lose data.
#
# State handling:
#   canonical missing + current_db real file  → promote file to canonical
#   canonical missing + source_db available   → seed canonical from source
#   canonical exists  + current_db real file  → back up file, create symlink
#   current_db already correct symlink        → no-op
ensure_db_symlinked() {
    local current_db="$1"
    local canonical_db="$2"
    local source_db="$3"

    if ! $DRY_RUN; then
        mkdir -p "$(dirname "$canonical_db")"
    fi

    # Step A: Populate canonical DB from the freshest versioned database.
    if [ ! -e "$canonical_db" ] && [ ! -L "$canonical_db" ]; then
        if [ -n "$source_db" ] && [ -f "$source_db" ]; then
            if [ "$source_db" = "$current_db" ] && [ ! -L "$current_db" ]; then
                if $DRY_RUN; then
                    log "[DRY-RUN] Would move: $current_db → $canonical_db"
                else
                    mv "$current_db" "$canonical_db"
                    log "Promoted DB to canonical:  $canonical_db"
                fi
            elif $DRY_RUN; then
                log "[DRY-RUN] Would copy: $source_db → $canonical_db"
            else
                cp "$source_db" "$canonical_db"
                log "Seeded canonical DB from:  $source_db"
            fi
        else
            vlog "No source DB found — canonical will be created on first CPA write."
        fi
    fi

    # Step B: Ensure current_db is a symlink pointing at canonical
    if [ -L "$current_db" ]; then
        local existing_target
        existing_target="$(readlink "$current_db")"
        # Normalize relative symlinks without requiring the target to exist.
        case "$existing_target" in
            /*) ;;
            *)  existing_target="$(python3 - "$current_db" "$existing_target" <<'PY'
import os
import sys

link_path = sys.argv[1]
link_target = sys.argv[2]
print(os.path.normpath(os.path.join(os.path.dirname(link_path), link_target)))
PY
)" ;;
        esac
        if [ "$existing_target" = "$canonical_db" ]; then
            log "DB symlink already correct: $current_db → $canonical_db"
            return 0
        fi
        # Wrong target — needs fixing
        if $DRY_RUN; then
            log "[DRY-RUN] Would fix symlink: $current_db (→ $existing_target) → $canonical_db"
        else
            rm -f "$current_db"
        fi
    elif [ -e "$current_db" ]; then
        # Real file at current_db — if canonical also exists, back up the file
        # to avoid silently discarding divergent data.
        if [ -e "$canonical_db" ]; then
            local backup="${current_db}.bak.$(date +%Y%m%d-%H%M%S)"
            warn "Real DB exists at $current_db AND canonical exists at $canonical_db"
            warn "Backing up the version-dir copy before symlinking."
            if $DRY_RUN; then
                log "[DRY-RUN] Would back up: $current_db → $backup, then symlink"
            else
                mv "$current_db" "$backup"
                log "Backed up divergent DB:    $backup"
            fi
        fi
    fi

    # Create the symlink if it doesn't exist (or was removed above)
    if [ ! -L "$current_db" ]; then
        if $DRY_RUN; then
            log "[DRY-RUN] Would symlink: $current_db → $canonical_db"
        else
            mkdir -p "$(dirname "$current_db")"
            ln -sf "$canonical_db" "$current_db"
            log "Created symlink:           $current_db → $canonical_db"
        fi
    fi
}

# ── Main migration logic ────────────────────────────────────────────────────

main() {
    local current_dir source_dylib source_db plugin_dir data_dir

    # ---- 1. Detect current CPA version ----
    current_dir="$(get_current_version_dir)"
    log "Current CPA version dir:   $current_dir"
    vlog "  (symlink: $UPSTREAM_DIR/current → $(readlink "$UPSTREAM_DIR/current"))"

    plugin_dir="$current_dir/plugins/$PLATFORM"
    data_dir="$current_dir/data"

    # ---- 2. Check if migration is needed ----
    local existing_in_current
    existing_in_current="$(find "$plugin_dir" -maxdepth 1 \
        -name "${PLUGIN_NAME}-v*.${DYLIB_EXT}" 2>/dev/null | head -1)" || true

    if [ -n "$existing_in_current" ]; then
        log "Plugin already present in current version: $(basename "$existing_in_current")"
        log "No migration needed."
        # Still check DB and config
    fi

    # ---- 3. Optionally build the current source version ----
    if $FORCE_BUILD; then
        local built_source_version built_artifact
        built_source_version="$(get_source_version)"
        built_artifact="$PLUGIN_SOURCE_DIR/dist/${PLUGIN_NAME}.${DYLIB_EXT}"
        log "Building source version:   v$built_source_version"
        if $DRY_RUN; then
            log "[DRY-RUN] Would run: make build VERSION=$built_source_version"
            log "[DRY-RUN] Would deploy: $built_artifact"
            source_dylib="$built_artifact"
        else
            make -C "$PLUGIN_SOURCE_DIR" build VERSION="$built_source_version"
            if [ ! -f "$built_artifact" ]; then
                err "Build succeeded but artifact was not found: $built_artifact"
                exit 1
            fi
            source_dylib="$built_artifact"
        fi
    fi

    # ---- 4. Find source dylib ----
    if [ -z "${source_dylib:-}" ]; then
        source_dylib="$(find_latest_dylib)"
    fi
    if [ -z "$source_dylib" ]; then
        err "No usage-keeper dylib found in any CPA version directory."
        err "Build and deploy one first, or use --force-build."
        exit 1
    fi
    log "Latest source dylib:       $source_dylib"

    local source_version
    if $FORCE_BUILD; then
        source_version="$(get_source_version)"
    else
        source_version="$(extract_dylib_version "$source_dylib")"
    fi

    # ---- 5. Copy dylib to current version ----
    local target_dylib="$plugin_dir/${PLUGIN_NAME}-v${source_version}.${DYLIB_EXT}"

    if [ -f "$target_dylib" ]; then
        # Compare checksums
        if cmp -s "$source_dylib" "$target_dylib" 2>/dev/null; then
            log "Dylib already up-to-date: $(basename "$target_dylib")"
        else
            log "Dylib differs — will overwrite."
        fi
    elif [ -n "$existing_in_current" ]; then
        # Different version exists in current — remove old, deploy new
        log "Removing old dylib: $(basename "$existing_in_current")"
        $DRY_RUN || rm -f "$existing_in_current"
    fi

    if [ ! -f "$target_dylib" ] || ! cmp -s "$source_dylib" "$target_dylib" 2>/dev/null; then
        if $DRY_RUN; then
            log "[DRY-RUN] Would copy: $source_dylib → $target_dylib"
        else
            mkdir -p "$plugin_dir"
            cp "$source_dylib" "$target_dylib"
            chmod 755 "$target_dylib"
            log "Dylib deployed:            $target_dylib"
        fi
    fi

    # ---- 5. Database: symlink to version-independent canonical location ----
    local current_db="$data_dir/usage-keeper.db"
    local canonical_db="$CANONICAL_DB_DIR/usage-keeper.db"
    source_db="$(find_latest_db)"

    log "Canonical DB location:     $canonical_db"
    if [ -e "$canonical_db" ]; then
        log "  canonical size:          $(du -h "$canonical_db" | cut -f1)"
    else
        vlog "  canonical does not exist yet"
    fi

    ensure_db_symlinked "$current_db" "$canonical_db" "$source_db"

    # ---- 6. Config check (symlink makes relative db_path safe) ----
    local config_db_path
    config_db_path="$(yaml_value db_path)"
    case "$config_db_path" in
        /*) vlog "db_path is absolute: $config_db_path" ;;
        '') vlog "db_path not set in config (plugin default applies)" ;;
        *)  vlog "db_path is relative '$config_db_path' — safe: symlinked to canonical" ;;
    esac

    # ---- 7. Hot-reload if CPA is running ----
    if is_cpa_running; then
        log "CPA is running. Triggering hot-reload..."
        if $DRY_RUN; then
            log "[DRY-RUN] Would toggle config to trigger hot-reload."
        else
            trigger_hot_reload
        fi
    else
        warn "CPA is not running. Please start CPA:"
        warn "  \"$current_dir/CLIProxyAPI\" -config \"$CONFIG_FILE\" > /tmp/cpa.log 2>&1 &"
    fi

    # ---- 8. Verify ----
    if ! $DRY_RUN && is_cpa_running; then
        log "Waiting for hot-reload to take effect..."
        sleep "$HOT_RELOAD_VERIFY_WAIT_SECONDS"
        if verify_plugin; then
            log "✓ Plugin verified — dashboard returns 200"
        else
            warn "✗ Plugin verification failed — dashboard not returning 200 yet."
            warn "  Try: curl http://localhost:18317/v0/resource/plugins/usage-keeper/dashboard"
        fi
    fi

    # ---- Summary ----
    echo ""
    echo "────────────────────────────────────────────────────────"
    if $DRY_RUN; then
        echo "  DRY-RUN complete. Re-run with --apply to execute."
    else
        echo "  Migration complete."
    fi
    echo ""
    echo "  Target CPA version:  $(basename "$current_dir")"
    echo "  Plugin dylib:        $(basename "$target_dylib")"
    echo "  Canonical DB:        $canonical_db"
    if [ -e "$canonical_db" ]; then
        echo "    size:              $(du -h "$canonical_db" | cut -f1)"
    else
        echo "    size:              (will be created on first CPA write)"
    fi
    if [ -L "$current_db" ]; then
        echo "  Version DB symlink:  $current_db"
        echo "    →                  $(readlink "$current_db")"
    elif [ -e "$current_db" ]; then
        echo "  Version DB (file):   $current_db"
    else
        echo "  Version DB:          (not linked yet)"
    fi
    echo "  Dashboard:           http://localhost:18317/v0/resource/plugins/usage-keeper/dashboard"
    echo "────────────────────────────────────────────────────────"
}

main
