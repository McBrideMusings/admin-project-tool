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
        *)
            echo "error: unknown option: $1" >&2
            echo "usage: $0 [--force] [--shell zsh|bash|sh]" >&2
            exit 1
            ;;
    esac
done

if ! python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)'; then
    echo "error: admin-project-tool requires Python 3.9+ (found: $(python3 --version 2>&1))" >&2
    exit 1
fi
if ! python3 -c 'import tomllib' 2>/dev/null && ! python3 -c 'import tomli' 2>/dev/null; then
    echo "error: tomllib not available for $(python3 --version 2>&1)" >&2
    echo "  Python 3.11+ has tomllib in stdlib. For Python 3.9/3.10:" >&2
    echo "  pip install tomli" >&2
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

    rm -f  "$DEST_DIR/init-admin-v2"
    rm -f  "$DEST_DIR/init-admin"
    rm -f  "$DEST_DIR/bundle"

    # The manifest frontend (manifest/archetypes/kinds/compile/detectors) now
    # lives inside admin_lib/ (copied above); only the archetype + detector DATA
    # dirs are top-level. rm the stale gen/ so an upgrade from a pre-move install
    # drops it.
    rm -rf "$DEST_DIR/gen" "$DEST_DIR/archetypes" "$DEST_DIR/detectors"
    cp -R "$SRC_DIR/archetypes" "$DEST_DIR/archetypes"
    cp -R "$SRC_DIR/detectors"  "$DEST_DIR/detectors"
    cp    "$SRC_DIR/admin-run"  "$DEST_DIR/admin"
    chmod +x "$DEST_DIR/admin"

    # Clean up artifacts from the pre-interpreter (generator) install: admin-gen,
    # the SessionStart hook, and the (now removed) shim templates are gone (ADR-0006).
    rm -f  "$DEST_DIR/admin-gen"
    rm -rf "$DEST_DIR/hooks" "$DEST_DIR/templates"

    echo "$CURRENT_SHA" > "$DEST_DIR/VERSION"
    echo "  admin_lib $LIB_VERSION installed (archetypes/, detectors/, admin)"
    echo "  VERSION=$CURRENT_SHA"
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

if [[ "$FORCE" == "1" ]]; then
    echo "admin-project-tool: force-installing to $DEST_DIR"
    do_install
elif [[ "$INSTALLED_SHA" == "none" ]]; then
    echo "admin-project-tool: not installed — installing to $DEST_DIR"
    do_install
elif [[ "$CURRENT_SHA" == "unknown" || "$INSTALLED_SHA" != "$CURRENT_SHA" ]]; then
    echo "admin-project-tool: installed ($INSTALLED_SHA) → updating to $CURRENT_SHA"
    do_install
elif [[ "$CURRENT_SHA" == *+dirty ]]; then
    echo "admin-project-tool: working tree is dirty — reinstalling"
    do_install
else
    echo "admin-project-tool: already up to date ($CURRENT_SHA)"
fi

verify_path

echo "Done."
