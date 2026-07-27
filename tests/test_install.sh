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

assert_symlink() {
    local desc="$1" path="$2"
    if [[ -L "$path" ]]; then ok "$desc"; else fail "$desc" "not a symlink: $path"; fi
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

# Path to the developer's global gitignore, resolved under the REAL HOME.
# install.sh decides "clean or dirty" with `git status --porcelain`, and the
# tests run it under a sandbox HOME that has neither ~/.gitconfig nor
# ~/.config/git/ignore. Without this, a file the developer's global ignore hides
# — a stray ./admin left by a local install — reads as untracked inside the
# sandbox, install.sh calls the tree dirty, and the up-to-date tests fail on a
# checkout that is actually clean.
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
    # Use --shell zsh so shell detection is deterministic regardless of the
    # shell running the test suite.
    # GIT_CONFIG_* (git 2.31+) carries the excludes file into every git call
    # install.sh makes, which HOME= alone cannot reach.
    if [[ -n "$GLOBAL_EXCLUDES" ]]; then
        HOME="$home_dir" \
        GIT_CONFIG_COUNT=1 \
        GIT_CONFIG_KEY_0=core.excludesFile \
        GIT_CONFIG_VALUE_0="$GLOBAL_EXCLUDES" \
            bash "$REPO/install.sh" --shell zsh "$@" 2>&1
    else
        HOME="$home_dir" bash "$REPO/install.sh" --shell zsh "$@" 2>&1
    fi
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
assert_file_exists "manifest frontend folded into admin_lib" "$T/.admin/admin_lib/manifest.py"
assert_file_exists "archetype data dir copied" "$T/.admin/archetypes"
assert_file_exists "detector data dir copied" "$T/.admin/detectors"
assert_file_exists "admin interpreter copied" "$T/.admin/admin"
assert_file_contains "PATH added to rc" '.admin' "$T/$ZSH_RC"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: already up to date (clean tree only)
# ---------------------------------------------------------------------------

echo
echo "--- already up to date ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
mkdir -p "$T/.admin"
SNAP_SHA="$(get_current_sha)"
echo "$SNAP_SHA" > "$T/.admin/VERSION"

out="$(run_install "$T")"
if [[ "$SNAP_SHA" == *+dirty ]]; then
    # Dirty tree always reinstalls — can't observe "already up to date" here.
    assert_contains "reports up to date" "dirty" "$out"
else
    assert_contains "reports up to date" "already up to date" "$out"
    assert_not_contains "does not say installing" "installing" "$out"
fi
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
# Test: dirty SHA in VERSION always reinstalls (not "already up to date")
# ---------------------------------------------------------------------------

echo
echo "--- dirty tree always reinstalls ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"
mkdir -p "$T/.admin"
FAKE_SHA="$(git -C "$REPO" rev-parse HEAD 2>/dev/null || echo unknown)+dirty"
echo "$FAKE_SHA" > "$T/.admin/VERSION"

out="$(run_install "$T")"
if [[ "$(get_current_sha)" == *+dirty ]]; then
    assert_not_contains "dirty SHA never says already up to date" "already up to date" "$out"
    assert_file_exists "admin_lib reinstalled" "$T/.admin/admin_lib"
else
    assert_contains "clean tree with dirty-version reinstalls" "updating" "$out"
    assert_file_exists "admin_lib reinstalled after dirty-version" "$T/.admin/admin_lib"
fi
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
# Test: --link install (symlinks instead of copying, so source edits are live)
# ---------------------------------------------------------------------------

echo
echo "--- --link install ---"
T="$(mktemp -d)"
touch "$T/$ZSH_RC"

out="$(run_install "$T" --link)"
assert_contains "reports linking" "linking" "$out"
assert_contains "reports edits are live" "edits are live" "$out"
assert_symlink "admin_lib symlinked" "$T/.admin/admin_lib"
assert_symlink "archetype data dir symlinked" "$T/.admin/archetypes"
assert_symlink "detector data dir symlinked" "$T/.admin/detectors"
assert_symlink "admin interpreter symlinked" "$T/.admin/admin"
assert_file_contains "VERSION records the linked source" "linked:$REPO" "$T/.admin/VERSION"

# An edit to the source tree must be visible through the link with no reinstall
# -- that is the entire point of the mode. Probe with a NEW throwaway file
# rather than appending to a tracked one: a `git checkout --` to undo that would
# silently destroy a developer's uncommitted work if the suite ran on a dirty tree.
probe_file="$REPO/admin_lib/_link_probe_$$.py"
echo "# link liveness probe" > "$probe_file"
if [[ -f "$T/.admin/admin_lib/_link_probe_$$.py" ]]; then
    ok "source edits are live through the link"
else
    fail "source edits are live through the link" "probe not visible via $T/.admin"
fi
rm -f "$probe_file"

# A plain install over a linked install must NOT silently revert to a copy --
# that would quietly reintroduce staleness.
out="$(run_install "$T")"
assert_contains "plain install reports it is linked" "edits are already live" "$out"
assert_symlink "plain install leaves the link intact" "$T/.admin/admin_lib"

# --force is the documented way back to a copied install.
out="$(run_install "$T" --force)"
assert_contains "force reports installing" "force-installing" "$out"
if [[ -L "$T/.admin/admin_lib" ]]; then
    fail "--force replaces the link with a real copy" "still a symlink"
else
    ok "--force replaces the link with a real copy"
fi
assert_file_exists "source tree survives unlinking" "$REPO/admin_lib/apple.py"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo
echo "=== Results: $PASS passed, $FAIL failed ==="
[[ "$FAIL" == "0" ]]
