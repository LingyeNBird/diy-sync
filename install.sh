#!/usr/bin/env bash
# Installs (or upgrades) fdsync, the WSL side, from the GitHub releases:
#
#   curl -fsSL https://raw.githubusercontent.com/LingyeNBird/diy-sync/HEAD/install.sh | bash
#
# Options (environment variables):
#   FDSYNC_VERSION=1.2.3     a specific version (default: the newest release)
#   FDSYNC_BIN_DIR=DIR       where to put it (default: ~/.local/bin)
#   FDSYNC_FORCE=1           reinstall even if already on the target version
#   FDSYNC_NO_RESTART=1      do not automatically restart a running daemon
set -euo pipefail

REPO="LingyeNBird/diy-sync"
BIN_DIR="${FDSYNC_BIN_DIR:-$HOME/.local/bin}"
VERSION="${FDSYNC_VERSION:-}"
VERSION="${VERSION#v}"
FORCE="${FDSYNC_FORCE:-}"
NO_RESTART="${FDSYNC_NO_RESTART:-}"

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

installed="$BIN_DIR/fdsync"
if [ -x "$installed" ]; then
  # Newer builds have a built-in `fdsync upgrade` command: delegate directly so
  # version checks, checksum validation and daemon restart follow one code path.
  if "$installed" upgrade --help >/dev/null 2>&1; then
    say "fdsync is already installed at $installed; running upgrade …"
    uargs=()
    [ -n "$VERSION" ] && uargs+=(--version "$VERSION")
    [ -n "$FORCE" ] && uargs+=(--force)
    [ -n "$NO_RESTART" ] && uargs+=(--no-restart)
    exec "$installed" upgrade "${uargs[@]}"
  fi

  # Upgrading from an older build (without `fdsync upgrade`): check whether we
  # are already on the target version before downloading.
  cur_ver="$("$installed" version 2>/dev/null | awk '{print $2}' || true)"
  cur_ver="${cur_ver#v}"
  target_ver="$VERSION"
  if [ -z "$target_ver" ]; then
    eff_url="$(curl -fsSLI --proto '=https' -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null || true)"
    case "$eff_url" in
      */releases/tag/v*) target_ver="${eff_url##*/releases/tag/v}" ;;
      */releases/tag/*) target_ver="${eff_url##*/releases/tag/}" ;;
    esac
  fi
  if [ -z "$FORCE" ] && [ -n "$cur_ver" ] && [ -n "$target_ver" ] && [ "$cur_ver" = "$target_ver" ]; then
    say "fdsync is already up to date ($cur_ver)."
    exit 0
  fi
  say "Upgrading fdsync${cur_ver:+ ($cur_ver → ${target_ver:-latest})} …"
fi

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

systemd_active=0
bg_running=0
if command -v systemctl >/dev/null 2>&1 && systemctl --user is-active --quiet fdsync 2>/dev/null; then
  systemd_active=1
elif pgrep -x fdsync >/dev/null 2>&1; then
  bg_running=1
fi

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

if [ "$systemd_active" -eq 1 ]; then
  if [ -n "$NO_RESTART" ]; then
    say "The systemd service is running the previous version: restart it with  systemctl --user restart fdsync"
  else
    say "Restarting systemd user service fdsync …"
    systemctl --user restart fdsync
  fi
elif [ "$bg_running" -eq 1 ]; then
  if [ -n "$NO_RESTART" ]; then
    say "The fdsync daemon is running the previous version: restart it with  fdsync stop && fdsync start"
  else
    say "Restarting fdsync daemon …"
    "$BIN_DIR/fdsync" stop || true
    "$BIN_DIR/fdsync" start
  fi
elif [ ! -e "${XDG_CONFIG_HOME:-$HOME/.config}/fdsync/credentials.json" ]; then
  say "Next: fdsync connect   (pair with the Windows server), then fdsync start"
fi
