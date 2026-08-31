#!/usr/bin/env bash
# Tests for uninstall.sh.
# Runs entirely in sandboxed temp directories — does not touch ~/.admin or real rc files.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
PASS=0
FAIL=0

# These tests run install.sh, which compiles the Go binary, and `go` derives its
# caches from HOME — which every test overrides to sandbox ~/.admin. Left alone,
# the module set lands in the temp HOME 0555 and the trailing `rm -rf "$T"`
# fails, killing the run under set -e. See tests/test_install.sh for the same fix.
export GOMODCACHE="$(go env GOMODCACHE)"
export GOCACHE="$(go env GOCACHE)"

# On macOS, zsh writes to .zprofile; on Linux, .zshrc.
if [[ "$(uname -s)" == "Darwin" ]]; then
    ZSH_RC=".zprofile"
else
    ZSH_RC=".zshrc"
fi

# ---------------------------------------------------------------------------
# Assertion helpers
# ---------------------------------------------------------------------------

ok() {
    echo "  PASS: $1"
    PASS=$((PASS + 1))
}

fail() {
    echo "  FAIL: $1"
    [[ $# -lt 2 ]] || echo "        $2"
    FAIL=$((FAIL + 1))
}

assert_contains() {
    local desc="$1" needle="$2" hay="$3"
    if [[ "$hay" == *"$needle"* ]]; then ok "$desc"; else fail "$desc" "expected substring: $needle"; fi
}

assert_not_exists() {
    local desc="$1" path="$2"
    if [[ ! -e "$path" ]]; then ok "$desc"; else fail "$desc" "still present: $path"; fi
}

assert_file_exists() {
    local desc="$1" path="$2"
    if [[ -e "$path" ]]; then ok "$desc"; else fail "$desc" "missing: $path"; fi
}

assert_file_not_contains() {
    local desc="$1" needle="$2" file="$3"
    if ! grep -qF "$needle" "$file" 2>/dev/null; then ok "$desc"; else fail "$desc" "unexpectedly found in $file: $needle"; fi
}

# Path to the developer's global gitignore, resolved under the REAL HOME —
# same reasoning as test_install.sh: without this, install.sh (run as part of
# test setup here) can misjudge the source tree as dirty inside the sandbox.
global_excludes() {
    local path
    path="$(git config --global --get core.excludesFile 2>/dev/null || true)"
    [[ -n "$path" ]] || path="${XDG_CONFIG_HOME:-$HOME/.config}/git/ignore"
    path="${path/#\~/$HOME}"
    [[ -f "$path" ]] && echo "$path"
}

GLOBAL_EXCLUDES="$(global_excludes)"

run_install() {
    local home_dir="$1"; shift
    if [[ -n "$GLOBAL_EXCLUDES" ]]; then
        HOME="$home_dir" \
        GIT_CONFIG_COUNT=1 \
        GIT_CONFIG_KEY_0=core.excludesFile \
        GIT_CONFIG_VALUE_0="$GLOBAL_EXCLUDES" \
            bash "$REPO/install.sh" --shell zsh "$@" >/dev/null 2>&1
    else
        HOME="$home_dir" bash "$REPO/install.sh" --shell zsh "$@" >/dev/null 2>&1
    fi
}

run_uninstall() {
    local home_dir="$1"; shift
    HOME="$home_dir" bash "$REPO/uninstall.sh" --shell zsh "$@" 2>&1
}

# ---------------------------------------------------------------------------
# Test: uninstall after a fresh install removes everything install.sh wrote
# ---------------------------------------------------------------------------

echo
echo "--- uninstall after fresh install ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
run_install "$T"

out="$(run_uninstall "$T")"
assert_contains "reports done" "Done." "$out"
assert_not_exists "admin_lib removed" "$T/.admin/admin_lib"
assert_not_exists "archetypes removed" "$T/.admin/archetypes"
assert_not_exists "detectors removed" "$T/.admin/detectors"
assert_not_exists "admin entrypoint removed" "$T/.admin/admin"
assert_not_exists "VERSION removed" "$T/.admin/VERSION"
assert_not_exists "empty .admin dir removed" "$T/.admin"
assert_file_not_contains "PATH entry removed from rc" '.admin' "$T/$ZSH_RC"
assert_file_not_contains "marker comment removed from rc" '# admin-project-tool' "$T/$ZSH_RC"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: uninstall after a --link install removes the links, not the source
# ---------------------------------------------------------------------------

echo
echo "--- uninstall after --link install ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
run_install "$T" --link

out="$(run_uninstall "$T")"
assert_not_exists "linked admin_lib removed" "$T/.admin/admin_lib"
assert_not_exists ".admin dir removed" "$T/.admin"
assert_file_exists "source tree survives uninstall" "$REPO/admin_lib/apple.py"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: uninstall with nothing installed is a no-op, not an error
# ---------------------------------------------------------------------------

echo
echo "--- uninstall with nothing installed ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"

out="$(run_uninstall "$T")"
assert_contains "reports nothing to uninstall" "nothing to uninstall" "$out"
assert_contains "still reports done" "Done." "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: unrelated content in ~/.admin survives uninstall
# ---------------------------------------------------------------------------

echo
echo "--- unrelated content in ~/.admin is preserved ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
run_install "$T"
echo "keep me" > "$T/.admin/unrelated-file"

run_uninstall "$T" >/dev/null
assert_file_exists "unrelated file survives" "$T/.admin/unrelated-file"
assert_not_exists "admin_lib still removed" "$T/.admin/admin_lib"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: running uninstall twice is safe (idempotent)
# ---------------------------------------------------------------------------

echo
echo "--- uninstall twice is idempotent ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
run_install "$T"
run_uninstall "$T" >/dev/null

out="$(run_uninstall "$T")"
assert_contains "second run reports nothing to uninstall" "nothing to uninstall" "$out"
assert_contains "second run still reports done" "Done." "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: --shell override selects the correct rc file
# ---------------------------------------------------------------------------

echo
echo "--- --shell override ---"
T="$(mktemp -d)"
touch "$T/.bashrc"
HOME="$T" bash "$REPO/install.sh" --shell bash >/dev/null 2>&1

out="$(HOME="$T" bash "$REPO/uninstall.sh" --shell bash 2>&1)"
assert_contains "PATH removed from .bashrc" "removed PATH entry from $T/.bashrc" "$out"
assert_file_not_contains "no PATH entry left in .bashrc" '.admin' "$T/.bashrc"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo
echo "=== Results: $PASS passed, $FAIL failed ==="
[[ "$FAIL" == "0" ]]
