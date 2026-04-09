#!/usr/bin/env bash
# Install admin_lib to ~/.admin/ for use by per-project admin scripts.
# Idempotent — safe to run multiple times.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
DEST_DIR="$HOME/.admin"

# v2 requires Python 3.11+ for stdlib tomllib.
if ! python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)'; then
    echo "error: admin-project-tool requires Python 3.11+ (for stdlib tomllib)" >&2
    echo "  found: $(python3 --version 2>&1)" >&2
    exit 1
fi

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

# v2: install generator package, archetypes, detectors, and init-admin-v2
rm -rf "$DEST_DIR/gen" "$DEST_DIR/archetypes" "$DEST_DIR/detectors"
cp -R "$SRC_DIR/gen" "$DEST_DIR/gen"
cp -R "$SRC_DIR/archetypes" "$DEST_DIR/archetypes"
cp -R "$SRC_DIR/detectors" "$DEST_DIR/detectors"
cp "$SRC_DIR/init-admin-v2" "$DEST_DIR/init-admin-v2"
chmod +x "$DEST_DIR/init-admin-v2"

# Record source commit SHA for provenance. The installed copy is not a git
# repo, so render.py reads this file instead of shelling out to git.
if git -C "$SRC_DIR" rev-parse HEAD >/dev/null 2>&1; then
    SHA="$(git -C "$SRC_DIR" rev-parse HEAD)"
    if [[ -n "$(git -C "$SRC_DIR" status --porcelain)" ]]; then
        SHA="${SHA}+dirty"
    fi
    echo "$SHA" > "$DEST_DIR/VERSION"
else
    echo "unknown" > "$DEST_DIR/VERSION"
fi
echo "Installed v2 generator (gen/, archetypes/, detectors/, init-admin-v2, VERSION=$(cat "$DEST_DIR/VERSION"))"

# Check if ~/.admin is in PATH
case ":$PATH:" in
    *":$DEST_DIR:"*) ;;
    *) echo ""
       echo "Hint: add ~/.admin to your PATH to use init-admin globally:"
       echo "  export PATH=\"\$HOME/.admin:\$PATH\""
       ;;
esac

echo "Done."
