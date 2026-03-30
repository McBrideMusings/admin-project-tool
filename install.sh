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

# Install templates
rm -rf "$DEST_DIR/templates"
cp -R "$SRC_DIR/templates" "$DEST_DIR/templates"
echo "Installed templates to $DEST_DIR/templates/"

# Install init-admin
cp "$SRC_DIR/init-admin" "$DEST_DIR/init-admin"
chmod +x "$DEST_DIR/init-admin"
echo "Installed init-admin to $DEST_DIR/init-admin"

# Install bundle
cp "$SRC_DIR/bundle" "$DEST_DIR/bundle"
chmod +x "$DEST_DIR/bundle"
echo "Installed bundle to $DEST_DIR/bundle"

# Check if ~/.admin is in PATH
case ":$PATH:" in
    *":$DEST_DIR:"*) ;;
    *) echo ""
       echo "Hint: add ~/.admin to your PATH to use init-admin globally:"
       echo "  export PATH=\"\$HOME/.admin:\$PATH\""
       ;;
esac

echo "Done."
