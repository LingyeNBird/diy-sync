#!/usr/bin/env bash
# Adds a commit on origin's master: commit.sh WORKDIR "message"
set -euo pipefail
W="$1"; rm -rf "$W/c"; git clone -q "$W/origin.git" "$W/c"; cd "$W/c"
git config user.email t@t; git config user.name t
echo "$2" >> CHANGES.txt; git add CHANGES.txt; git commit -qm "$2"; git push -q origin master
