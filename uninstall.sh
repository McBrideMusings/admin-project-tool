#!/usr/bin/env bash
# Remove admin-project-tool from ~/.admin/ and undo the PATH entry install.sh
# added to the shell rc file.
#
# Usage: ./uninstall.sh [--shell zsh|bash|sh]
#
# Targets exactly what install.sh writes today (ADR-0006): $DEST_DIR/{admin_lib,
# archetypes,detectors,admin,VERSION} plus the "# admin-project-tool" PATH block
# it appends to the rc file. Anything else under $DEST_DIR is left alone.
#
# Pre-ADR-0006 leftovers (templates/, hooks/, gen/, bundle, init-admin,
# init-admin-v2, admin-gen) are not targeted here — install.sh itself deletes
# them on the next `./install.sh` run (do_install/do_link), so any checkout
# that has already been reinstalled since the cutover has none of them left.
# A checkout uninstalled without ever re-running install.sh since before the
# cutover could still have them; that's a one-time manual cleanup, not this
# script's job.

set -euo pipefail

DEST_DIR="$HOME/.admin"
SHELL_OVERRIDE=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --shell)
            [[ $# -gt 1 ]] || { echo "error: --shell requires an argument" >&2; exit 1; }
            SHELL_OVERRIDE="$2"
            shift 2
            ;;
        *)
            echo "error: unknown option: $1" >&2
            echo "usage: $0 [--shell zsh|bash|sh]" >&2
            exit 1
            ;;
    esac
done

# ------------------------------------------------------------------
# Shell / rc-file detection — mirrors install.sh's shell_rc() so uninstall
# targets the same rc file a matching install would have written to.
# ------------------------------------------------------------------
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

shell_rc() {
    local sh
    sh="$(_active_shell)"
    case "$sh" in
        zsh)
            # macOS Terminal and most macOS launchers open login shells, which
            # source ~/.zprofile but skip ~/.zshrc — same reasoning as install.sh.
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

# ------------------------------------------------------------------
# Remove installed artifacts — exactly what install.sh's do_install/do_link
# write to $DEST_DIR. rm -rf with no trailing slash on a symlinked dir removes
# the link itself, never the source tree it points at (a --link install).
# ------------------------------------------------------------------
if [[ ! -e "$DEST_DIR" ]]; then
    echo "admin-project-tool: $DEST_DIR does not exist — nothing to uninstall"
else
    rm -rf "$DEST_DIR/admin_lib" "$DEST_DIR/archetypes" "$DEST_DIR/detectors"
    rm -f  "$DEST_DIR/admin" "$DEST_DIR/VERSION"
    echo "  removed $DEST_DIR/{admin_lib,archetypes,detectors,admin,VERSION}"

    # Leave any unrelated content in $DEST_DIR alone; only remove the
    # directory itself if install.sh's artifacts were all it contained.
    if rmdir "$DEST_DIR" 2>/dev/null; then
        echo "  removed empty $DEST_DIR"
    else
        echo "  $DEST_DIR left in place (not empty)"
    fi
fi

# ------------------------------------------------------------------
# Remove the PATH block install.sh appends via verify_path():
#   (blank line)
#   # admin-project-tool
#   export PATH="$HOME/.admin:$PATH"
# ------------------------------------------------------------------
RC="$(shell_rc)"

if [[ -f "$RC" ]] && grep -qF '# admin-project-tool' "$RC"; then
    awk '
        { buf[NR] = $0 }
        END {
            n = NR
            for (i = 1; i <= n; i++) {
                if (buf[i] == "# admin-project-tool") {
                    del_line[i] = 1
                    if (i + 1 <= n && buf[i + 1] == "export PATH=\"$HOME/.admin:$PATH\"") {
                        del_line[i + 1] = 1
                    }
                    if (i - 1 >= 1 && buf[i - 1] == "") {
                        del_line[i - 1] = 1
                    }
                }
            }
            for (i = 1; i <= n; i++) if (!(i in del_line)) print buf[i]
        }
    ' "$RC" > "$RC.uninstall-tmp"
    mv "$RC.uninstall-tmp" "$RC"
    echo "  removed PATH entry from $RC"
else
    echo "  no PATH entry found in $RC"
fi

echo "Done."
