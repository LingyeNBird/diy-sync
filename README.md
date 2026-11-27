# fdsync

Selective two-way sync between Windows folders and WSL folders over HTTP,
instead of the slow `/mnt/c` 9P mount. Design: [docs/requirements.md](docs/requirements.md).

| Part | Where |
|---|---|
| `fdsync-server.exe` (Windows) | `cmd/fdsync-server`, `internal/server` |
| `fdsync` daemon + CLI (WSL) | `cmd/fdsync`, `internal/client` |
| Shared: rules, index, scanner, watcher, file ops | `internal/{rules,index,scan,watch,fsx,syncer,vv,protocol}` |
| Management panel (React, embedded into the server via `internal/panel`) | `web/` |
| Windows behaviour check | `cmd/fdsync-wintest` |

## Status

Working: two-way sync between the server and any number of WSL clients
(scope rules, exclusions, version vectors, tombstones, conflicts with
"use Windows / use WSL / keep both", a trash of at most 50 MB per side,
watchers and rescans), and the management panel on live data: two-sided
file browser with previews and diffs (the WSL side is read on demand),
rule editor, conflicts, bindings and folders created from the panel,
activity, settings, English and Simplified Chinese. Clients pair with
`fdsync connect` (OAuth device flow) over TLS with a pinned certificate.
Large files move block by block (only changed blocks travel); names that
differ only in case are resolved as the spec says.

Also: rename and move detection (files are moved, not sent again), HTTP/2
with parallel transfers, a per-folder "newest wins" conflict policy and
pause, "pause all" for a while, a trash page (search, compare versions,
restore), log files capped at 2 × 5 MB. On Windows the server runs in the
background with a tray icon and can start at login.

## Install

In WSL (puts `fdsync` in `~/.local/bin` and adds that folder to `PATH` in `~/.bashrc`):

```sh
curl -fsSL https://raw.githubusercontent.com/LingyeNBird/diy-sync/HEAD/install.sh | bash
```

Run it again to upgrade (or just run `fdsync upgrade` once installed — it
checks for newer releases, verifies `SHA256SUMS`, swaps the binary and restarts
the daemon if it was running). `FDSYNC_VERSION=1.2.3` installs a given version
and `FDSYNC_BIN_DIR=DIR` picks another folder, e.g.
`curl -fsSL …/install.sh | FDSYNC_VERSION=1.2.3 bash`.

On Windows, download `fdsync-server-windows-amd64.exe` from the
[releases](https://github.com/LingyeNBird/diy-sync/releases) and run it. After that,
the panel's Settings → Check for updates finds new versions; Upgrade downloads
one, checks its checksum, swaps the exe and restarts it.

## Release

Actions → **Build and release** → Run workflow: give the version as `1.2.3`
(no `v`). With "Publish a GitHub release" off it only builds (the files are
the run's artifact); on, it also tags the built commit `v1.2.3`, pushes
the tag, and creates the release with notes, the commits since the previous
release (or since the first commit) and a Full Changelog link.

- The release or the tag (at another commit) already exists: the run stops
  before building. A tag already at the built commit is reused (a re-run).
- The tag push fails: tried 3 times, then the run stops without a release.
- The release fails after the tag was pushed: the tag (and a half-made
  draft) is deleted again, so the run can simply be repeated.

- "Previous release" and "Latest" go by version, not date: 1.4.3 after
  2.0.0 lists the changes since 1.4.2 and doesn't become Latest (which the
  upgrade check and `install.sh` download).

The tag logic is `.github/scripts/tag.sh`, the notes `.github/scripts/notes.sh`.
`bash .github/test-release/scenarios.sh` runs the whole workflow locally
against a fake GitHub (every path: first release, re-runs, existing tags,
an older-line fix, a failed upload) before you change it.
The upgrade check and `install.sh` read the releases without a token, so they
need a public repository. Files: `fdsync-server-windows-{amd64,arm64}.exe`,
`fdsync-server-linux-amd64`, `fdsync-linux-{amd64,arm64}`, `SHA256SUMS`.

## Build

```sh
export PATH=$HOME/.local/go/bin:$PATH
cd web && pnpm build && cd ..   # the panel, embedded into the server
go test ./...
./build.sh                      # the panel and every exe into dist/ (the server exe without a console window)
cd web && pnpm test:rules && pnpm test:events   # shared rule cases; every server event has a message
```

## Try it

On Windows:

```bat
fdsync-server.exe add-folder projects C:\Users\me\projects "a/" "c/" "!c/**/node_modules/"
fdsync-server.exe
```

The server has no window: it shows an icon in the notification area (open
panel, pause all, start with Windows, quit). On the first start it opens the
panel at http://127.0.0.1:7444 so you can set its password; starting it again
while it runs just opens the panel. The log is `%LOCALAPPDATA%\fdsync\server.log`.
Subcommands such as `show-config` still print to the terminal they're run
from; `--no-tray` runs it without the icon.
fdsync keeps its index, staging folder and trash in that data directory, never inside the synced folders.

In WSL:

```sh
cp dist/fdsync ~/.local/bin/             # any folder on PATH
fdsync connect                           # the Windows host; check the fingerprint, approve the code in the panel
fdsync start                             # sync in the background (fdsync stop; fdsync status shows it)
```

`connect` without an address uses the Windows host (WSL's default gateway)
and follows it when it changes after a restart. To start the daemon with
the first shell, add `fdsync start >/dev/null` to `~/.profile` (it does nothing
when the daemon already runs; the log is `~/.local/state/fdsync/fdsync.log`).
`fdsync daemon` runs it in the terminal instead.
With systemd enabled in WSL, `fdsync install-service` sets up a user service instead.

`--bind` is optional: bindings can also be created in the panel
(Connections → the client → New binding); the daemon remembers them.
