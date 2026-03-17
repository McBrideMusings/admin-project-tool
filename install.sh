#!/usr/bin/env bash
# Install admin_lib to ~/.admin/ for use by per-project admin scripts.
# Idempotent — safe to run multiple times.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
DEST_DIR="$HOME/.admin"

echo "Installing admin_lib to $DEST_DIR..."

mkdir -p "$DEST_DIR"

# Remove old version and copy fresh
rm -rf "$DEST_DIR/admin_lib"
cp -R "$SRC_DIR/admin_lib" "$DEST_DIR/admin_lib"

echo "Installed admin_lib $(python3 -c "
import sys; sys.path.insert(0, '$DEST_DIR')
from admin_lib import __version__; print(__version__)
")"

echo "Done."
