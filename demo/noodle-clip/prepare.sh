#!/bin/bash
# Build the demo project: a throwaway git repo whose admin.toml is manifest.toml.
# The directory name is the project name the dashboard header shows.
# usage: prepare.sh <dest-dir>
set -euo pipefail
src="$(cd "$(dirname "$0")" && pwd)"
dest="$1"
rm -rf "$dest"
mkdir -p "$dest"
cp -R "$src/scripts" "$dest/scripts"
cp "$src/manifest.toml" "$dest/admin.toml"
printf 'tmp/\nadmin.toml\n' >"$dest/.gitignore"
git -C "$dest" init -q -b main
git -C "$dest" add .gitignore scripts
git -C "$dest" -c user.name=demo -c user.email=demo@example.com commit -q -m "init"
