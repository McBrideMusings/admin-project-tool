#!/usr/bin/env bash
# Tests for hooks/session-admin-check.sh.
# Runs entirely in temp directories — does not touch ~/.admin or real projects.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
HOOK="$REPO/hooks/session-admin-check.sh"
PASS=0
FAIL=0

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

assert_eq() {
    local desc="$1" expected="$2" actual="$3"
    if [[ "$expected" == "$actual" ]]; then ok "$desc"; else fail "$desc" "expected=$expected actual=$actual"; fi
}

assert_empty() {
    local desc="$1" val="$2"
    if [[ -z "$val" ]]; then ok "$desc"; else fail "$desc" "expected empty, got: $val"; fi
}

assert_not_empty() {
    local desc="$1" val="$2"
    if [[ -n "$val" ]]; then ok "$desc"; else fail "$desc" "expected non-empty output"; fi
}

assert_valid_json() {
    local desc="$1" val="$2"
    if python3 -c "import json,sys; json.loads(sys.argv[1])" "$val" 2>/dev/null; then
        ok "$desc"
    else
        fail "$desc" "not valid JSON: $val"
    fi
}

assert_contains() {
    local desc="$1" needle="$2" hay="$3"
    if [[ "$hay" == *"$needle"* ]]; then ok "$desc"; else fail "$desc" "expected substring: $needle"; fi
}

# Run the hook with a given HOME dir and optional stdin JSON.
# Usage: run_hook <home_dir> [json_stdin]
run_hook() {
    local home_dir="$1"
    local stdin_json="${2:-}"
    if [[ -n "$stdin_json" ]]; then
        HOME="$home_dir" bash "$HOOK" <<< "$stdin_json" 2>&1
    else
        HOME="$home_dir" bash "$HOOK" </dev/null 2>&1
    fi
}

run_hook_rc() {
    local home_dir="$1"
    local stdin_json="${2:-}"
    if [[ -n "$stdin_json" ]]; then
        HOME="$home_dir" bash "$HOOK" <<< "$stdin_json" 2>&1; echo $?
    else
        HOME="$home_dir" bash "$HOOK" </dev/null 2>&1; echo $?
    fi
    true
}

# Create a fake admin-gen at $T/.admin/admin-gen with given exit behaviour.
# $1 = home dir, $2 = audit_exit, $3 = regen_exit
make_admin_gen() {
    local home_dir="$1" audit_exit="$2" regen_exit="$3"
    mkdir -p "$home_dir/.admin"
    cat > "$home_dir/.admin/admin-gen" <<EOF
#!/usr/bin/env bash
case "\$1" in
    --audit)      exit $audit_exit ;;
    --regenerate) exit $regen_exit ;;
esac
exit 0
EOF
    chmod +x "$home_dir/.admin/admin-gen"
}

# ---------------------------------------------------------------------------
# Test: non-admin project (no admin.toml) → silent exit 0
# ---------------------------------------------------------------------------

echo
echo "--- non-admin project ---"
T="$(mktemp -d)"
make_admin_gen "$T" 0 0

out="$(run_hook "$T" '{"cwd":"'"$T"'"}')"
rc_line="$(HOME="$T" bash "$HOOK" <<< '{"cwd":"'"$T"'"}' 2>&1; echo $?)"
rc="${rc_line##*$'\n'}"
assert_empty "no output for non-admin project" "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: admin.toml present but no ./admin file → silent exit 0
# ---------------------------------------------------------------------------

echo
echo "--- admin.toml without ./admin ---"
T="$(mktemp -d)"
touch "$T/admin.toml"
make_admin_gen "$T" 0 0

out="$(run_hook "$T" '{"cwd":"'"$T"'"}')"
assert_empty "no output when ./admin missing" "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: missing admin-gen → silent exit 0
# ---------------------------------------------------------------------------

echo
echo "--- missing admin-gen ---"
T="$(mktemp -d)"
touch "$T/admin.toml"
touch "$T/admin"

out="$(run_hook "$T" '{"cwd":"'"$T"'"}')"
assert_empty "no output when admin-gen missing" "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: clean project (audit exits 0) → no output, exit 0
# ---------------------------------------------------------------------------

echo
echo "--- clean project (no drift) ---"
T="$(mktemp -d)"
touch "$T/admin.toml"
touch "$T/admin"
make_admin_gen "$T" 0 0

out="$(run_hook "$T" '{"cwd":"'"$T"'"}')"
assert_empty "no output when audit is clean" "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: drift detected, regen succeeds → valid JSON output
# ---------------------------------------------------------------------------

echo
echo "--- drift detected, regen succeeds ---"
T="$(mktemp -d)"
touch "$T/admin.toml"
touch "$T/admin"
make_admin_gen "$T" 2 0

out="$(run_hook "$T" '{"cwd":"'"$T"'"}')"
assert_not_empty "output emitted on regen" "$out"
assert_valid_json "output is valid JSON" "$out"
assert_contains "output mentions additionalContext" "additionalContext" "$out"
assert_contains "output mentions auto-regenerated" "auto-regenerated" "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: drift detected, regen fails (dirty tree etc.) → no output, exit 0
# ---------------------------------------------------------------------------

echo
echo "--- drift detected, regen fails ---"
T="$(mktemp -d)"
touch "$T/admin.toml"
touch "$T/admin"
make_admin_gen "$T" 2 1

out="$(run_hook "$T" '{"cwd":"'"$T"'"}')"
assert_empty "no output when regen fails" "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: no stdin (empty / closed) → falls back to PWD, exits 0
# ---------------------------------------------------------------------------

echo
echo "--- no stdin fallback ---"
T="$(mktemp -d)"
make_admin_gen "$T" 0 0

out="$(HOME="$T" PWD="$T" bash "$HOOK" </dev/null 2>&1)"
assert_empty "no output on no-stdin fallback to non-admin PWD" "$out"
rm -rf "$T"

# ---------------------------------------------------------------------------
# Test: cwd parsed from JSON stdin (not $PWD)
# ---------------------------------------------------------------------------

echo
echo "--- cwd from JSON stdin ---"
T="$(mktemp -d)"
PROJ="$(mktemp -d)"
touch "$PROJ/admin.toml"
touch "$PROJ/admin"
make_admin_gen "$T" 2 0

out="$(HOME="$T" bash "$HOOK" <<< '{"cwd":"'"$PROJ"'"}'  2>&1)"
assert_not_empty "project in JSON stdin is picked up" "$out"
assert_valid_json "JSON from JSON-stdin project is valid" "$out"
rm -rf "$T" "$PROJ"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo
echo "=== Results: $PASS passed, $FAIL failed ==="
[[ "$FAIL" == "0" ]]
