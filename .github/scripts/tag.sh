#!/usr/bin/env bash
# Creates the release tag vVERSION at COMMIT and pushes it to origin.
#
#   tag.sh VERSION COMMIT
#   tag.sh --check VERSION COMMIT   only fail if the tag names another commit
#
# - The tag already on origin at COMMIT (a re-run after a failed release):
#   reused, nothing pushed.
# - The tag on origin at another commit: fails (the files were built from
#   COMMIT, so the tag must not point elsewhere).
# - The push fails: retried; if someone else pushed the same tag meanwhile,
#   it is checked like an existing one.
#
# Writes created=true|false to $GITHUB_OUTPUT (true = this run pushed it,
# so a failed release may delete it again).
set -euo pipefail

check_only=false
if [ "${1:-}" = --check ]; then
  check_only=true
  shift
fi
version="$1"
commit="$2"
tag="v$version"
out="${GITHUB_OUTPUT:-/dev/null}"
attempts="${TAG_PUSH_ATTEMPTS:-3}"
delay="${TAG_RETRY_DELAY:-5}"

err() { echo "::error::$*" >&2; exit 1; }

# The commit the tag names on origin ("" = no such tag). An annotated tag
# is listed twice: the tag object, and "^{}" with the commit it points at.
remote_commit() {
  local lines
  lines=$(git ls-remote --tags origin "refs/tags/$tag" "refs/tags/$tag^{}") || return 1
  local peeled plain
  peeled=$(awk -v r="refs/tags/$tag^{}" '$2 == r { print $1 }' <<<"$lines")
  plain=$(awk -v r="refs/tags/$tag" '$2 == r { print $1 }' <<<"$lines")
  echo "${peeled:-$plain}"
}

# Fails unless an existing tag names the built commit.
check_existing() {
  if [ "$1" = "$commit" ]; then
    echo "Tag $tag already exists at $commit: using it."
    echo "created=false" >>"$out"
    exit 0
  fi
  err "Tag $tag already exists at $1, but this run built $commit. Delete the tag (git push origin :refs/tags/$tag) or use another version."
}

if $check_only; then
  existing=$(remote_commit) || err "Could not list the tags on origin"
  if [ -n "$existing" ] && [ "$existing" != "$commit" ]; then
    err "Tag $tag already exists at $existing, but this run builds $commit"
  fi
  exit 0
fi

commit=$(git rev-parse --verify "$commit^{commit}") || err "No commit $2"

# Pull the current tags, so a stale local copy can't mislead.
git fetch --quiet --force --tags origin || err "Could not fetch the tags from origin"

existing=$(remote_commit) || err "Could not list the tags on origin"
[ -n "$existing" ] && check_existing "$existing"

git config user.name >/dev/null || git config user.name "github-actions[bot]"
git config user.email >/dev/null || git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git tag -d "$tag" >/dev/null 2>&1 || true
git tag -a "$tag" -m "fdsync $version" "$commit" || err "Could not create tag $tag"

for ((i = 1; i <= attempts; i++)); do
  if git push --quiet origin "refs/tags/$tag"; then
    echo "Pushed tag $tag at $commit."
    echo "created=true" >>"$out"
    exit 0
  fi
  echo "::warning::Pushing tag $tag failed (attempt $i of $attempts)"
  # Someone else may have pushed it meanwhile.
  if existing=$(remote_commit) && [ -n "$existing" ]; then
    check_existing "$existing"
  fi
  ((i < attempts)) && sleep "$delay"
done
err "Could not push tag $tag to origin (a tag rule may block it, or the token lacks contents: write)"
