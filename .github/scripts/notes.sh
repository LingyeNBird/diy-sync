#!/usr/bin/env bash
# Prints the release notes for vVERSION built from COMMIT: a description,
# how to install, the commits since the previous release (or since the
# first commit) and a Full Changelog link.
#
#   notes.sh VERSION COMMIT REPO   (REPO = owner/name; needs gh and the full history)
set -euo pipefail

version="$1"
commit="$2"
repo="$3"
tag="v$version"
repo_url="${GITHUB_SERVER_URL:-https://github.com}/$repo"

# The release just below this version (not the newest by date).
prev=$(bash "$(dirname "$0")/releases.sh" previous "$version" "$repo")
if [ -n "$prev" ] && git rev-parse -q --verify "refs/tags/$prev^{commit}" >/dev/null; then
  from="$prev"
  range="$prev..$commit"
  since="since $prev"
else
  [ -n "$prev" ] && echo "::warning::Previous release $prev has no tag here; listing all commits" >&2
  from=$(git rev-list --max-parents=0 "$commit" | tail -n 1)
  range="$commit"
  since="since the first commit"
fi

cat <<EOF
fdsync $version: selective two-way sync between Windows folders and WSL folders.

### Install

- **Windows:** download \`fdsync-server-windows-amd64.exe\` (or \`-arm64\`) and run it.
  A running server upgrades itself: panel → Settings → Check for updates.
- **WSL:**

  \`\`\`sh
  curl -fsSL https://raw.githubusercontent.com/$repo/HEAD/install.sh | bash
  \`\`\`

\`SHA256SUMS\` lists the checksum of every file.

### Changes $since

EOF
git log --no-merges --format='- %s (%h)' "$range"
echo
echo "**Full Changelog**: $repo_url/compare/$from...$tag"
