#!/usr/bin/env bash
# Tests for install.sh branching logic.
# Runs entirely in sandboxed temp directories — does not touch ~/.admin or real rc files.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
PASS=0
FAIL=0

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

assert_not_contains() {
    local desc="$1" needle="$2" hay="$3"
    if [[ "$hay" != *"$needle"* ]]; then ok "$desc"; else fail "$desc" "unexpected substring: $needle"; fi
}

assert_file_exists() {
    local desc="$1" path="$2"
    if [[ -e "$path" ]]; then ok "$desc"; else fail "$desc" "missing: $path"; fi
}

assert_file_contains() {
    local desc="$1" needle="$2" file="$3"
    if grep -qF "$needle" "$file" 2>/dev/null; then ok "$desc"; else fail "$desc" "not found in $file: $needle"; fi
}

assert_file_not_contains() {
    local desc="$1" needle="$2" file="$3"
    if ! grep -qF "$needle" "$file" 2>/dev/null; then ok "$desc"; else fail "$desc" "unexpectedly found in $file: $needle"; fi
}

assert_eq() {
    local desc="$1" expected="$2" actual="$3"
    if [[ "$expected" == "$actual" ]]; then ok "$desc"; else fail "$desc" "expected=$expected actual=$actual"; fi
}

# ---------------------------------------------------------------------------
# Test helpers
# ---------------------------------------------------------------------------

get_current_sha() {
    local sha
    sha="$(git -C "$REPO" rev-parse HEAD 2>/dev/null || echo "unknown")"
    [[ -n "$(git -C "$REPO" status --porcelain 2>/dev/null)" ]] && sha="${sha}+dirty"
    echo "$sha"
}

run_install() {
    local home_dir="$1"; shift
    # Use --shell zsh so shell detection is deterministic regardless of the
    # shell running the test suite.
    HOME="$home_dir" bash "$REPO/install.sh" --shell zsh "$@" 2>&1
}

# ---------------------------------------------------------------------------
# Test: fresh install (not installed)
# ---------------------------------------------------------------------------

echo
echo "--- fresh install ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"

out="$(run_install "$T")"
assert_contains "reports not-installed" "not installed" "$out"
assert_contains "reports done" "Done." "$out"
assert_file_exists "VERSION written" "$T/.admin/VERSION"
assert_file_exists "admin_lib copied" "$T/.admin/admin_lib"
assert_file_exists "gen copied" "$T/.admin/gen"
assert_file_exists "admin-gen copied" "$T/.admin/admin-gen"
assert_file_contains "PATH added to rc" '.admin' "$T/$ZSH_RC"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: already up to date
# ---------------------------------------------------------------------------

echo
echo "--- already up to date ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
mkdir -p "$T/.admin"
echo "$(get_current_sha)" > "$T/.admin/VERSION"

out="$(run_install "$T")"
assert_contains "reports up to date" "already up to date" "$out"
assert_not_contains "does not say installing" "installing" "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: stale install (different SHA)
# ---------------------------------------------------------------------------

echo
echo "--- stale install ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
mkdir -p "$T/.admin"
echo "oldshaxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" > "$T/.admin/VERSION"

out="$(run_install "$T")"
assert_contains "reports updating" "updating" "$out"
assert_file_exists "admin_lib updated" "$T/.admin/admin_lib"

NEW_VER="$(cat "$T/.admin/VERSION")"
if [[ "$NEW_VER" != "oldshaxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" ]]; then
    ok "VERSION updated from old SHA"
else
    fail "VERSION updated from old SHA" "still old"
fi
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: --force reinstalls even when already up to date
# ---------------------------------------------------------------------------

echo
echo "--- --force flag ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
mkdir -p "$T/.admin"
echo "$(get_current_sha)" > "$T/.admin/VERSION"

out="$(run_install "$T" --force)"
assert_contains "reports force-installing" "force-installing" "$out"
assert_file_exists "admin_lib present after --force" "$T/.admin/admin_lib"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: PATH entry not duplicated on second install
# ---------------------------------------------------------------------------

echo
echo "--- PATH deduplication ---"
T="$(mktemp -d)"
printf '\n# admin-project-tool\nexport PATH="$HOME/.admin:$PATH"\n' > "$T/$ZSH_RC"

run_install "$T" >/dev/null
out="$(run_install "$T")"
assert_contains "reports already present" "already present" "$out"

COUNT="$(grep -c '.admin:' "$T/$ZSH_RC" 2>/dev/null || echo 0)"
assert_eq "PATH entry not duplicated" "1" "$COUNT"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: --shell override selects correct rc file
# ---------------------------------------------------------------------------

echo
echo "--- --shell override ---"
T="$(mktemp -d)"
touch "$T/.bashrc"

HOME="$T" bash "$REPO/install.sh" --shell bash >/dev/null 2>&1 || true
assert_file_contains "PATH added to .bashrc" '.admin' "$T/.bashrc"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: verify_path checks rc file even when $DEST_DIR is already on PATH
# ---------------------------------------------------------------------------

echo
echo "--- verify_path checks rc file regardless of session PATH ---"
T="$(mktemp -d)"
mkdir -p "$T/.admin"

# Up-to-date install, but rc file has no PATH entry.
echo "$(get_current_sha)" > "$T/.admin/VERSION"
touch "$T/$ZSH_RC"  # rc file exists but is empty

# Inject DEST_DIR onto PATH so the session-PATH check would have passed under
# the old verify_path logic (which skipped ensure_path_in_rc when on PATH).
out="$(PATH="$T/.admin:$PATH" HOME="$T" bash "$REPO/install.sh" --shell zsh 2>&1)"
assert_file_contains "rc file written even when on session PATH" '.admin' "$T/$ZSH_RC"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo
echo "=== Results: $PASS passed, $FAIL failed ==="
[[ "$FAIL" == "0" ]]
