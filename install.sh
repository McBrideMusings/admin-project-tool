#!/usr/bin/env bash
# Install admin to ~/.admin/. Smart install: detects existing version, skips
# reinstall when up to date.
#
# Usage: ./install.sh [--link] [--force] [--shell zsh|bash|sh]
#
# `admin` is the Go binary (main.go + cmd/ + internal/), built here and dropped
# at ~/.admin/admin. A compiled binary cannot be symlinked back to source, so it
# is rebuilt on EVERY run, --link included.
#
# `admin-py` is the outgoing Python interpreter, installed alongside it. It
# exists only to cover the verbs the Go binary does not carry yet — `new` and
# `compile` — and is deleted along with admin_lib/ when admin-3rgp.30 closes.
# Do not add callers.
#
# Default installs a COPY of the Python half, so edits to this source tree do
# not affect admin-py until the next install. --link symlinks instead. Once
# linked, a plain install leaves the links alone; --force switches back to a
# copy. Neither mode changes how the binary is handled.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
DEST_DIR="$HOME/.admin"

# ------------------------------------------------------------------
# Argument parsing
# ------------------------------------------------------------------

FORCE=0
LINK=0
SHELL_OVERRIDE=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --force|-f)
            FORCE=1
            shift
            ;;
        --link|-l)
            LINK=1
            shift
            ;;
        --shell)
            [[ $# -gt 1 ]] || { echo "error: --shell requires an argument" >&2; exit 1; }
            SHELL_OVERRIDE="$2"
            shift 2
            ;;
        *)
            echo "error: unknown option: $1" >&2
            echo "usage: $0 [--link] [--force] [--shell zsh|bash|sh]" >&2
            exit 1
            ;;
    esac
done

# ------------------------------------------------------------------
# Refuse to run inside a linked worktree.
# ------------------------------------------------------------------
# install.sh installs from whatever checkout it's run in, and --link points
# ~/.admin/admin_lib at that checkout's admin_lib. A worktree's --git-dir is
# <primary>/.git/worktrees/<name>, distinct from its --git-common-dir
# <primary>/.git — the primary checkout has the two equal. Run from a
# worktree and this script would silently replace the GLOBAL admin on every
# project on the machine with an in-progress branch, and --link would leave
# ~/.admin/admin_lib dangling the moment that worktree is deleted.
GIT_DIR="$(git -C "$SRC_DIR" rev-parse --git-dir 2>/dev/null || true)"
GIT_COMMON_DIR="$(git -C "$SRC_DIR" rev-parse --git-common-dir 2>/dev/null || true)"
if [[ -n "$GIT_DIR" && -n "$GIT_COMMON_DIR" ]]; then
    # rev-parse returns absolute paths under -C; resolve relative ones
    # (older git, or run without -C) against SRC_DIR instead of joining blindly.
    [[ "$GIT_DIR" == /* ]] || GIT_DIR="$SRC_DIR/$GIT_DIR"
    [[ "$GIT_COMMON_DIR" == /* ]] || GIT_COMMON_DIR="$SRC_DIR/$GIT_COMMON_DIR"
    GIT_DIR="$(cd "$GIT_DIR" && pwd)"
    GIT_COMMON_DIR="$(cd "$GIT_COMMON_DIR" && pwd)"
    if [[ "$GIT_DIR" != "$GIT_COMMON_DIR" ]]; then
        PRIMARY_CHECKOUT="$(dirname "$GIT_COMMON_DIR")"
        echo "error: install.sh was run from a linked worktree ($SRC_DIR)" >&2
        echo "  Installing from a worktree would replace the global admin on PATH" >&2
        echo "  with this in-progress branch, for every project on the machine —" >&2
        echo "  and leave it dangling once the worktree is deleted." >&2
        echo "  Run it from the primary checkout instead:" >&2
        echo "    bash $PRIMARY_CHECKOUT/install.sh$([[ "$LINK" == 1 ]] && echo ' --link')" >&2
        exit 1
    fi
fi

echo "admin-project-tool: installing from $SRC_DIR"

# Go is required: it builds `admin` itself.
if ! command -v go >/dev/null 2>&1; then
    echo "error: admin-project-tool requires the Go toolchain to build admin" >&2
    echo "  install it: https://go.dev/dl/  (or: brew install go)" >&2
    exit 1
fi

# Python is optional and on its way out — it only installs admin-py. A machine
# without it gets the binary and a note, never a failed install.
WANT_PY=1
if ! python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)' 2>/dev/null; then
    WANT_PY=0
    PY_SKIP_REASON="Python 3.9+ not found"
elif ! python3 -c 'import tomllib' 2>/dev/null && ! python3 -c 'import tomli' 2>/dev/null; then
    WANT_PY=0
    PY_SKIP_REASON="tomllib unavailable for $(python3 --version 2>&1) (pip install tomli)"
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

# Build the Go binary straight onto $DEST_DIR/admin. Called by every branch of
# the main logic including "already linked" and "already up to date": source
# edits reach admin_lib live through a symlink, but a binary only changes when
# something compiles it, so skipping this is how ~/.admin/admin goes stale.
build_binary() {
    mkdir -p "$DEST_DIR"

    # rm first: from a pre-Go install $DEST_DIR/admin is a symlink to admin-run,
    # and `go build -o` would follow it and write the binary over the source file.
    rm -f "$DEST_DIR/admin"
    ( cd "$SRC_DIR" && go build -o "$DEST_DIR/admin" . )
    chmod +x "$DEST_DIR/admin"
    echo "  admin (go) built -> $DEST_DIR/admin"
}

# admin-py: the outgoing Python interpreter. Covers `new` and `compile`, the two
# verbs the Go binary does not carry. Goes away with admin-3rgp.30.
install_python() {
    local mode="$1"   # copy | link

    rm -rf "$DEST_DIR/admin_lib"
    rm -f  "$DEST_DIR/admin-py"

    if [[ "$WANT_PY" != "1" ]]; then
        echo "  admin-py skipped: $PY_SKIP_REASON"
        return
    fi

    if [[ "$mode" == "link" ]]; then
        ln -s "$SRC_DIR/admin_lib" "$DEST_DIR/admin_lib"
        ln -s "$SRC_DIR/admin-run" "$DEST_DIR/admin-py"
    else
        cp -R "$SRC_DIR/admin_lib" "$DEST_DIR/admin_lib"
        cp    "$SRC_DIR/admin-run" "$DEST_DIR/admin-py"
        chmod +x "$DEST_DIR/admin-py"
    fi

    LIB_VERSION="$(python3 -c "
import sys; sys.path.insert(0, '$DEST_DIR')
from admin_lib import __version__; print(__version__)
")"
    echo "  admin-py $LIB_VERSION ${mode}ed -> $DEST_DIR/admin-py"
}

# Artifacts from installs this one supersedes: the pre-interpreter generator
# (admin-gen, hooks/, templates/), the pre-move gen/ dir, and the old bundler.
clean_stale() {
    rm -f  "$DEST_DIR/init-admin-v2" "$DEST_DIR/init-admin" \
           "$DEST_DIR/bundle" "$DEST_DIR/admin-gen"
    rm -rf "$DEST_DIR/gen" "$DEST_DIR/hooks" "$DEST_DIR/templates"
}

do_install() {
    mkdir -p "$DEST_DIR"
    clean_stale

    rm -rf "$DEST_DIR/archetypes" "$DEST_DIR/detectors"
    cp -R "$SRC_DIR/archetypes" "$DEST_DIR/archetypes"
    cp -R "$SRC_DIR/detectors"  "$DEST_DIR/detectors"

    build_binary
    install_python copy

    echo "$CURRENT_SHA" > "$DEST_DIR/VERSION"
    echo "  archetypes/ and detectors/ copied"
    echo "  VERSION=$CURRENT_SHA"
}

do_link() {
    mkdir -p "$DEST_DIR"
    clean_stale

    # No trailing slashes: rm -rf on a symlink removes the link, never the
    # source tree it points at.
    rm -rf "$DEST_DIR/archetypes" "$DEST_DIR/detectors"
    ln -s "$SRC_DIR/archetypes" "$DEST_DIR/archetypes"
    ln -s "$SRC_DIR/detectors"  "$DEST_DIR/detectors"

    build_binary
    install_python link

    echo "linked:$SRC_DIR" > "$DEST_DIR/VERSION"
    echo "  archetypes/ and detectors/ linked"
    echo "  $DEST_DIR -> $SRC_DIR — Python edits are live; admin is rebuilt each install"
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

IS_LINKED=0
[[ -L "$DEST_DIR/admin_lib" ]] && IS_LINKED=1

if [[ "$LINK" == "1" ]]; then
    echo "admin-project-tool: linking $DEST_DIR to $SRC_DIR"
    do_link
elif [[ "$IS_LINKED" == "1" && "$FORCE" != "1" ]]; then
    # Don't silently revert a linked install back to a copy — that would quietly
    # reintroduce the staleness this mode exists to remove. The binary is still
    # rebuilt: nothing about a symlinked admin_lib recompiles Go.
    echo "admin-project-tool: linked to $(readlink "$DEST_DIR/admin_lib") — Python edits are already live"
    build_binary
    install_python link
    echo "  (--force to switch back to a copied install)"
elif [[ "$FORCE" == "1" ]]; then
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
    build_binary
fi

verify_path

echo "Done."
