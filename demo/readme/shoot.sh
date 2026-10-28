#!/bin/bash
# Regenerate the README screenshots in docs/images/ from the demo project.
# Builds admin from this checkout, prepares the demo in $ADMIN_SHOTS_DIR, drives it with vhs.
# usage: demo/readme/shoot.sh
set -euo pipefail
repo="$(cd "$(dirname "$0")/../.." && pwd)"
work="${ADMIN_SHOTS_DIR:-/tmp/admin-readme}"
rm -rf "$work"
mkdir -p "$work/bin"
go -C "$repo" build -o "$work/bin/admin" .
bash "$repo/demo/noodle-clip/prepare.sh" "$work/atlas"
export ADMIN_SHOTS_BIN="$work/bin" ADMIN_SHOTS_DEMO="$work/atlas"
mkdir -p "$repo/docs/images"
cd "$repo/docs/images"
vhs "$repo/demo/readme/shots.tape"
rm -f shots.gif
