#!/bin/sh
# Builds everything into dist/: the panel, fdsync-server.exe (no console
# window: it runs in the tray), the Windows check exe and the WSL fdsync.
set -e
cd "$(dirname "$0")"
# VERSION=1.2.3 ./build.sh stamps a version (default "dev"); releases are
# built by .github/workflows/release.yml.
export PATH="$HOME/.local/go/bin:$PATH"
ver="-X fdsync/internal/update.Version=${VERSION:-dev}"
(cd web && pnpm -s build >/dev/null)
GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui $ver" -o dist/fdsync-server.exe ./cmd/fdsync-server
GOOS=windows GOARCH=amd64 go build -o dist/fdsync-wintest.exe ./cmd/fdsync-wintest
go build -ldflags "$ver" -o dist/fdsync ./cmd/fdsync
go build -ldflags "$ver" -o dist/fdsync-server ./cmd/fdsync-server # for testing on Linux
ls -l dist
