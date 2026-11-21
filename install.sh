#!/usr/bin/env bash
# Installs (or updates) fdsync, the WSL side, from the GitHub releases:
#
#   curl -fsSL https://raw.githubusercontent.com/__GITHUB_REPO__/HEAD/install.sh | bash
#
# Options (environment variables):
#   FDSYNC_VERSION=1.2.3     a specific version (default: the newest release)
#   FDSYNC_BIN_DIR=DIR       where to put it (default: ~/.local/bin)
set -euo pipefail

REPO="__GITHUB_REPO__"
BIN_DIR="${FDSYNC_BIN_DIR:-$HOME/.local/bin}"
VERSION="${FDSYNC_VERSION:-}"
VERSION="${VERSION#v}"

say() { printf '%s\n' "$*"; }
die() { printf 'fdsync install: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "this installs the WSL/Linux side; on Windows download fdsync-server from https://github.com/$REPO/releases"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "no build for $(uname -m)" ;;
esac
command -v curl >/dev/null || die "curl is needed"
command -v sha256sum >/dev/null || die "sha256sum is needed (coreutils)"

asset="fdsync-linux-$arch"
if [ -n "$VERSION" ]; then
  base="https://github.com/$REPO/releases/download/v$VERSION"
else
  base="https://github.com/$REPO/releases/latest/download"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "Downloading $asset ${VERSION:+$VERSION }from github.com/$REPO …"
curl -fL --progress-bar --proto '=https' --retry 3 -o "$tmp/$asset" "$base/$asset" || die "download failed: $base/$asset"
curl -fsSL --proto '=https' --retry 3 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || die "download failed: $base/SHA256SUMS"
(cd "$tmp" && grep -E "[[:space:]]\*?$asset\$" SHA256SUMS | sha256sum -c --quiet -) || die "checksum mismatch"

mkdir -p "$BIN_DIR"
# Into place by rename: a running daemon keeps its old file until it restarts.
install -m 0755 "$tmp/$asset" "$BIN_DIR/.fdsync.new"
mv -f "$BIN_DIR/.fdsync.new" "$BIN_DIR/fdsync"
say "Installed $("$BIN_DIR/fdsync" version) to $BIN_DIR/fdsync"

# Put BIN_DIR on PATH for new shells.
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    rc="$HOME/.bashrc"
    dir="$BIN_DIR"
    case "$dir" in "$HOME"/*) dir="\$HOME/${dir#"$HOME"/}" ;; esac
    line="export PATH=\"$dir:\$PATH\""
    if ! grep -qsF "$line" "$rc"; then
      printf '\n# fdsync (added by its install script)\n%s\n' "$line" >>"$rc"
      say "Added $BIN_DIR to PATH in $rc"
    fi
    say "Open a new shell, or run:  $line"
    ;;
esac

if pgrep -x fdsync >/dev/null 2>&1; then
  say "The fdsync daemon is running the previous version: restart it with  fdsync stop && fdsync start"
elif [ ! -e "${XDG_CONFIG_HOME:-$HOME/.config}/fdsync/credentials.json" ]; then
  say "Next: fdsync connect   (pair with the Windows server), then fdsync start"
fi
