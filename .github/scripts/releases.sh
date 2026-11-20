#!/usr/bin/env bash
# Questions about the published releases (drafts and pre-releases don't count).
#
#   releases.sh previous VERSION REPO   the highest release below VERSION, e.g. v1.2.2 ("" = none)
#   releases.sh latest VERSION REPO     true if VERSION is above every release, else false
#
# By version, not by date: a fix for an older line (1.4.3 after 2.0.0) has
# 1.4.2 as its previous release and must not become "Latest", which the
# updater and install.sh download.
set -euo pipefail

cmd="$1"
version="$2"
repo="$3"

# Every published X.Y.Z release except VERSION itself, without the v.
others=$(gh release list --repo "$repo" --exclude-drafts --exclude-pre-releases --limit 1000 \
  --json tagName --jq '.[].tagName' |
  sed -n 's/^v\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$/\1/p' |
  grep -vxF "$version" || true)

case "$cmd" in
  previous)
    # Sort VERSION in with the others and take the one just below it.
    prev=$(printf '%s\n' $others "$version" | sort -V | sed -n "/^${version//./\\.}\$/{x;p;q};h")
    [ -n "$prev" ] && echo "v$prev"
    true
    ;;
  latest)
    top=$(printf '%s\n' $others "$version" | sort -V | tail -n 1)
    [ "$top" = "$version" ] && echo true || echo false
    ;;
  *)
    echo "usage: releases.sh previous|latest VERSION REPO" >&2
    exit 2
    ;;
esac
