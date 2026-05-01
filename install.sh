#!/usr/bin/env bash
# Install admin_lib to ~/.admin/ for use by per-project admin scripts.
# Smart install: detects existing version, skips reinstall when up to date.
#
# Usage: ./install.sh [--force] [--shell zsh|bash|sh]

set -euo pipefail

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
DEST_DIR="$HOME/.admin"

# ------------------------------------------------------------------
# Argument parsing
# ------------------------------------------------------------------

FORCE=0
SHELL_OVERRIDE=""
REGISTER_HOOKS_ONLY=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --force|-f)
            FORCE=1
            shift
            ;;
        --shell)
            [[ $# -gt 1 ]] || { echo "error: --shell requires an argument" >&2; exit 1; }
            SHELL_OVERRIDE="$2"
            shift 2
            ;;
        --register-hooks)
            REGISTER_HOOKS_ONLY=1
            shift
            ;;
        *)
            echo "error: unknown option: $1" >&2
            echo "usage: $0 [--force] [--register-hooks] [--shell zsh|bash|sh]" >&2
            exit 1
            ;;
    esac
done

if ! python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)'; then
    echo "error: admin-project-tool requires Python 3.11+ (for stdlib tomllib)" >&2
    echo "  found: $(python3 --version 2>&1)" >&2
    exit 1
fi

# ------------------------------------------------------------------
# Determine current source SHA
# ------------------------------------------------------------------
get_git_sha() {
    local dir="$1" sha
    if ! git -C "$dir" rev-parse HEAD >/dev/null 2>&1; then
        echo "unknown"
        return
    fi
    sha="$(git -C "$dir" rev-parse HEAD)"
    [[ -n "$(git -C "$dir" status --porcelain)" ]] && sha="${sha}+dirty"
    echo "$sha"
}

CURRENT_SHA="$(get_git_sha "$SRC_DIR")"
INSTALLED_SHA="none"
[[ -f "$DEST_DIR/VERSION" ]] && INSTALLED_SHA="$(cat "$DEST_DIR/VERSION")"

# ------------------------------------------------------------------
# Helpers
# ------------------------------------------------------------------

do_install() {
    mkdir -p "$DEST_DIR"

    rm -rf "$DEST_DIR/admin_lib"
    cp -R "$SRC_DIR/admin_lib" "$DEST_DIR/admin_lib"

    LIB_VERSION="$(python3 -c "
import sys; sys.path.insert(0, '$DEST_DIR')
from admin_lib import __version__; print(__version__)
")"

    rm -rf "$DEST_DIR/templates"
    rm -f  "$DEST_DIR/init-admin-v2"
    rm -f  "$DEST_DIR/init-admin"
    rm -f  "$DEST_DIR/bundle"

    rm -rf "$DEST_DIR/gen" "$DEST_DIR/archetypes" "$DEST_DIR/detectors"
    cp -R "$SRC_DIR/gen"        "$DEST_DIR/gen"
    cp -R "$SRC_DIR/archetypes" "$DEST_DIR/archetypes"
    cp -R "$SRC_DIR/detectors"  "$DEST_DIR/detectors"
    cp    "$SRC_DIR/admin-gen"  "$DEST_DIR/admin-gen"
    chmod +x "$DEST_DIR/admin-gen"

    mkdir -p "$DEST_DIR/hooks"
    cp "$SRC_DIR/hooks/session-admin-check.sh" "$DEST_DIR/hooks/session-admin-check.sh"
    chmod +x "$DEST_DIR/hooks/session-admin-check.sh"

    echo "$CURRENT_SHA" > "$DEST_DIR/VERSION"
    echo "  admin_lib $LIB_VERSION installed (gen/, archetypes/, detectors/, admin-gen, hooks/)"
    echo "  VERSION=$CURRENT_SHA"
}

register_claude_hook() {
    python3 - <<'PYEOF'
import json, os, sys

settings_path = os.path.expanduser("~/.claude/settings.json")
hook_cmd = os.path.expanduser("~/.admin/hooks/session-admin-check.sh")

if not os.path.exists(settings_path):
    print("  Claude Code not detected — skipping SessionStart hook registration")
    sys.exit(0)

if not os.path.exists(hook_cmd):
    print("  Warning: hook script not found at ~/.admin/hooks/ — skipping registration")
    sys.exit(0)

try:
    with open(settings_path) as f:
        config = json.load(f)
except (json.JSONDecodeError, OSError) as e:
    print(f"  Warning: could not read {settings_path}: {e}")
    sys.exit(0)

hooks = config.setdefault("hooks", {})
session_hooks = hooks.setdefault("SessionStart", [])

already = any(
    any(h.get("command") == hook_cmd for h in block.get("hooks", []))
    for block in session_hooks
)

if already:
    sys.exit(0)

session_hooks.append({"matcher": "", "hooks": [{"type": "command", "command": hook_cmd}]})

try:
    tmp = settings_path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(config, f, indent=2)
    os.replace(tmp, settings_path)
    print("  SessionStart hook registered in ~/.claude/settings.json")
except OSError as e:
    print(f"  Warning: could not write {settings_path}: {e}")
PYEOF
}

# Detect the shell running this script. Prefers ps(1) over $SHELL because
# $SHELL reflects the user's login shell, not the shell that invoked install.sh.
# Falls back to $SHELL if ps fails (e.g. minimal containers).
_active_shell() {
    if [[ -n "$SHELL_OVERRIDE" ]]; then
        echo "$SHELL_OVERRIDE"
        return
    fi
    local comm
    comm="$(ps -p $PPID -o comm= 2>/dev/null)" || true
    comm="${comm##*/}"  # strip path prefix
    comm="${comm#-}"    # strip leading dash (login shell marker)
    if [[ -n "$comm" ]]; then
        echo "$comm"
    else
        echo "${SHELL##*/}"
    fi
}

# Return the rc file that new interactive sessions will source.
shell_rc() {
    local sh
    sh="$(_active_shell)"
    case "$sh" in
        zsh)
            # macOS Terminal and most macOS launchers open login shells, which
            # source ~/.zprofile but skip ~/.zshrc. Write there so the PATH
            # export takes effect in new terminals on a default macOS setup.
            if [[ "$(uname -s)" == "Darwin" ]]; then
                echo "$HOME/.zprofile"
            else
                echo "$HOME/.zshrc"
            fi
            ;;
        bash) echo "$HOME/.bashrc" ;;
        *)    echo "$HOME/.profile" ;;
    esac
}

verify_path() {
    local rc
    rc="$(shell_rc)"

    if grep -qF '$HOME/.admin:$PATH' "$rc" 2>/dev/null; then
        echo "  PATH entry already present in $rc"
    else
        printf '\n# admin-project-tool\n%s\n' 'export PATH="$HOME/.admin:$PATH"' >> "$rc"
        echo "  Added PATH entry to $rc"
        echo "  Run: source $rc  (or open a new terminal)"
    fi

    if [[ ":${PATH}:" == *":$DEST_DIR:"* ]]; then
        echo "  $DEST_DIR is on PATH in this session"
    else
        echo "  Note: $DEST_DIR not on PATH in this session — open a new terminal"
    fi
}

# ------------------------------------------------------------------
# Main logic
# ------------------------------------------------------------------

if [[ "$REGISTER_HOOKS_ONLY" == "1" ]]; then
    register_claude_hook
    echo "Done."
    exit 0
fi

if [[ "$FORCE" == "1" ]]; then
    echo "admin-project-tool: force-installing to $DEST_DIR"
    do_install
elif [[ "$INSTALLED_SHA" == "none" ]]; then
    echo "admin-project-tool: not installed — installing to $DEST_DIR"
    do_install
elif [[ "$CURRENT_SHA" == "unknown" || "$INSTALLED_SHA" != "$CURRENT_SHA" ]]; then
    echo "admin-project-tool: installed ($INSTALLED_SHA) → updating to $CURRENT_SHA"
    do_install
else
    echo "admin-project-tool: already up to date ($CURRENT_SHA)"
fi

verify_path
register_claude_hook

echo "Done."
