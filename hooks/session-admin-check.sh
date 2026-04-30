#!/usr/bin/env bash
# Claude Code SessionStart hook: silently regenerates ./admin if out-of-date.
# Fires at the start of each Claude Code session; exits 0 always so it never
# blocks Claude from starting.

ADMIN_GEN="$HOME/.admin/admin-gen"

# Pre-read stdin before spawning python3 so the subprocess never blocks on
# an interactive or empty stream (e.g. when testing the hook manually).
input="$(cat 2>/dev/null || true)"

# Parse cwd from the JSON Claude sends on stdin.
cwd="$(printf '%s' "$input" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('cwd', ''))
except Exception:
    print('')
")"

[ -z "$cwd" ] && cwd="$PWD"

# No-op if this project doesn't use the admin tool, or admin-gen isn't installed.
if [ ! -f "$cwd/admin.toml" ] || [ ! -f "$cwd/admin" ] || [ ! -x "$ADMIN_GEN" ]; then
    exit 0
fi

# Audit: exit 0 = clean, exit 2 = drift, exit 1 = error.
"$ADMIN_GEN" --audit "$cwd" >/dev/null 2>&1
audit_rc=$?

if [ "$audit_rc" -eq 2 ]; then
    if "$ADMIN_GEN" --regenerate "$cwd" >/dev/null 2>&1; then
        # Literal-only string — never interpolate variables here without JSON-escaping first.
        printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"Note: ./admin was auto-regenerated (admin-project-tool was updated since last generation)."}}\n'
    fi
fi

exit 0
