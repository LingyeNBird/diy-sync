#!/usr/bin/env bash
# Fresh fake GitHub: origin.git holds the current working tree as one commit,
# with every executable bit stripped (like a copy that went through Windows
# or the web uploader), and no releases.
set -euo pipefail
W="$1"; SRC=$(cd "$(dirname "$0")/../.." && pwd)
rm -rf "$W"; mkdir -p "$W/seed"
cd "$SRC"
git ls-files -co --exclude-standard -z | grep -zv '^web/node_modules/' | xargs -0 cp --parents -t "$W/seed"
cd "$W/seed"
find . -type f -exec chmod a-x {} +
git init -q -b master; git config user.email t@t; git config user.name t; git config core.fileMode true
git add -A; git commit -qm "initial import"
git ls-files -s .github install.sh | awk '{print "  mode", $1, $4}'
git clone -q --bare . "$W/origin.git"
