# fdsync: requirements and design

Status: draft v1 (2026-11-19). Working name: **fdsync**. The directory name `fuse-diy-sync` is historical; FUSE is not used.

## 1. Goal

Two-way, rule-based sync between folders on Windows and folders inside one or more WSL instances. It runs over a custom HTTP(S) protocol, because WSL's 9P mount (`/mnt/c`) is too slow.

Non-goals: on-demand placeholder files, a client-side UI, running as a Windows service, FUSE.

## 2. Terms

| Term | Meaning |
|---|---|
| **Server** | `fdsync-server.exe` on Windows. Holds all configuration and the management panel. |
| **Client** | `fdsync` on a WSL instance. In the panel, one client = one **connection**. |
| **Sync folder** | A Windows folder that the server manages. |
| **Binding** | A link between a sync folder and a local folder on one client. One client can have several bindings. Several clients can bind the same sync folder. |
| **Scope** | The rule set of a sync folder: which paths take part in sync. It is shared by all bindings of that folder. |
| **Private item** | Anything outside the scope. It belongs only to its own side. |
| **Cancel synchronization** | Take a path out of scope. Files stay on disk on every side. |
| **Delete on one side** | An explicit admin action that deletes a path on one chosen side. |

The word "remove" is not used anywhere: not in the UI, the API or the docs.

## 3. Architecture

```
 Windows                                          WSL instance(s)
┌──────────────────────────────┐   HTTPS        ┌───────────────────────┐
│ fdsync-server.exe            │◄──────────────►│ fdsync daemon         │
│  ├ sync API   (LAN addr:port)│  index deltas, │  ├ watcher (inotify)  │
│  ├ panel      (127.0.0.1:port)│  blocks, ctrl  │  ├ index DB           │
│  ├ watcher (ReadDirChangesW) │                │  └ scanner            │
│  ├ index DB per sync folder  │                └───────────────────────┘
│  └ embedded web UI (go:embed)│                 fdsync connect / status (CLI)
└──────────────────────────────┘
```

- Written in Go. Each side is a single static binary. The panel frontend is embedded with `go:embed`.
- Topology is **hub and spoke**. The Windows folder is the reference copy. A change from client 1 goes to the server and from there to client 2. Clients never talk to each other.
- Server: a plain exe. The data directory defaults to `%LOCALAPPDATA%\fdsync` and can be changed with `--data-dir`. With `--open`, it opens the panel in the browser at startup.
- Client: `fdsync daemon` is the long-running agent (a systemd user unit is provided for WSL with systemd; it can also be started by hand). `fdsync connect <url>` pairs the client with a server, and `fdsync status` shows its state. Everything else is managed from the panel. This follows the same daemon-plus-CLI split as `tailscaled`/`tailscale`.

## 4. Scope and visibility

### 4.1 Rules
- Each sync folder has one ordered rule list. A rule includes a path (allowlist), or excludes it with a `!` prefix.
- Rules can be any depth and use glob wildcards: `*`, `**`, `?`, `[abc]`, `[!abc]`. The Go matcher (`internal/rules`) and the panel's matcher (`web/src/lib/rules.ts`) must agree; both run the shared cases in `internal/rules/testdata/cases.json`.
- The last matching rule wins, as in gitignore. Example:
  ```
  a/
  c/
  !c/**/node_modules/
  src/**/*.go
  ```
- The **auto-exclusion list** (§6) is separate. Entries are added by the system, each with a reason and an origin. It always takes priority over the rules.
- Private items never appear in the rules. That is how each client keeps its own folders even though the scope is shared.
- Consequence of sharing: if the admin adds client 1's private `d/` to the scope, it also syncs to client 2. If client 2 already has its own `d/`, that produces a scope-add conflict (§5.4).

### 4.2 Visibility
- A client receives index entries and content **only for in-scope paths**. Private Windows items never leave the server.
- A client reports its **full** file tree to the server as metadata only (path, size, mtime, type, no content). The panel shows the client's private items so they can be added to the scope. Content of a private item is fetched on demand only (preview or diff), and nothing is written to Windows disk until the path is in scope.

## 5. Sync model

This follows Syncthing's design.

### 5.1 Index
Each side keeps one index entry per file and per folder:

`path, type, size, mtime, mode, content-hash (BLAKE3), block-hashes, version-vector, deleted-flag, sequence`

- **Version vector:** a list of `{deviceID: counter}` recording which device changed the file and how often.
- **Sequence:** increases on every local index change.
- Storage: `bbolt`, one file per sync folder or binding.

### 5.2 Detecting changes
- `fsnotify` watchers (inotify on Linux, ReadDirectoryChangesW on Windows). Changes are batched with a 1 s delay (configurable).
- A periodic full rescan (default 60 min) catches anything the watchers missed. If the inotify watch limit is hit, the client logs it and the panel shows a warning with the `sysctl` fix.
- Before hashing, mtime + size are compared to skip unchanged files.
- Files received from the other side are written to a temp file in the staging folder (§8), hash-checked, fsynced, then renamed into place. The index records the file as it is on disk after the rename, so the next scan sees no change and nothing is sent back (no echo loop).
- **Non-intrusive:** fdsync puts nothing of its own in a synced folder: no metadata folder, no marker files, so nothing needs a `.gitignore` entry on either side. The index, staging folder and trash live in the data directory (`%LOCALAPPDATA%\fdsync\folders\<id>\` on Windows, `~/.local/state/fdsync/<folder>-<hash>/` in WSL). Only when the data directory is on a different drive than the folder are incoming files received next to their destination, under a `.fdsync-tmp-*` name, for the moment of the transfer. The server logs a warning when that happens.

### 5.3 Reconciling two versions
- If one version vector includes all the changes of the other, the newer version wins and is applied.
- If the vectors are concurrent but the content hash is the same, the vectors are merged. No conflict.
- If the vectors are concurrent and the content differs, the path goes into the **conflict queue**.
- If one side edited and the other deleted, the path goes into the conflict queue too. Suggested default: keep the edited version.
- Deletes are stored as **tombstones** (`deleted=true` plus a version vector) and sync like any other change. Tombstones are purged after 30 days, and only once every binding has acknowledged them.
- Renames: a delete and a create with the same hash in the same batch are sent as a rename, so the content isn't transferred again.

### 5.4 Conflicts
There are two sources:
- **Concurrent edits:** both sides changed the same in-scope file (§5.3).
- **Scope-add conflicts:** a path is added to the scope while both sides already hold different content there.

Behavior:
- While a file is in conflict, each side keeps its **own version in place**. Nothing is copied, renamed or duplicated on disk. Only that file is held. Everything else, including other files in the same folder, keeps syncing.
- The panel's diff view shows:
  - the tree, with a conflict marker on each file;
  - a side-by-side text diff, an image comparison, and a hex/metadata comparison for binary files;
  - the content of the other side, streamed on demand and never stored.
- Admin decisions: **use Windows**, **use WSL (client X)**, or **keep both**. "Keep both" saves the client version as `name.wsl-<client>.ext`. A decision can apply to one file, to every file in a folder, or to the whole conflict queue.
- **As soon as a file is resolved, it syncs.** There is no waiting for the rest of the folder.
- An optional per-folder auto policy ("newest wins") is available for folders where nobody cares which version is kept. It is off by default.

### 5.5 Offline and reconnecting
- Sync is based on state, not on a queue of operations. Each side remembers the last sequence number it received from the other side.
- On reconnect, each side sends the index entries changed since that sequence number, and §5.3 decides the outcome. A crash or a lost index only costs a rescan.
- Connection loss uses exponential backoff, capped at 60 s.

### 5.6 Transfer
- Files from 1 MiB up are split into blocks (128 KiB to 16 MiB, about 2,000 per file), each with a 128-bit BLAKE3 hash stored in the index.
- Only blocks the receiver doesn't already have in its old version are transferred: downloads use HTTP Range requests for runs of missing blocks; uploads send only the blocks the server lists (`Need.missing`). The whole-file hash is verified after assembling, so a reused block can never go unnoticed.
- Uses HTTP/2 with several streams in parallel. Optional zstd compression per folder, off by default.

## 6. Special files

These are added to the auto-exclusion list automatically. They are marked in the panel and are not synced in either direction:

- Symlinks on WSL. Junctions, symlinks and other reparse points on Windows. If either side has one at a path, that path is excluded for both sides.
- FIFOs, sockets, device files.
- Names Windows doesn't allow:
  - the characters `< > : " \ | ? *` and control characters 0x00–0x1F
  - reserved names (`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, `LPT1`–`LPT9`), also with an extension
  - names ending in a dot or a space
- Case collisions: the version already synced wins. If neither is synced yet, the one with the older mtime wins, then alphabetical order. The other one is excluded.
- Long paths (over 260 characters) are **not** excluded. The server uses `\\?\` paths.

Other metadata:
- The Unix mode (exec bit and so on) is stored in the index and kept on WSL clients. Files created on Windows get 0644, folders 0755.
- Hardlinks are synced as separate ordinary files.

## 7. Security

- **Pairing** follows the OAuth 2.0 Device Authorization Grant (RFC 8628):
  1. `fdsync connect https://192.168.1.10:7443` shows a verification link and a user code.
  2. The admin opens the link, logs into the panel, checks the code, names the connection and approves it.
  3. The client gets an access token (lifetime 1 h) and a refresh token.
- The admin can revoke a connection in the panel, which invalidates its tokens.
- **TLS is on by default.** It works like Syncthing and SSH trust-on-first-use:
  - the server creates a self-signed certificate on first run;
  - `fdsync connect` shows its fingerprint;
  - the client pins that fingerprint.
- `--plain-http` turns TLS off for trusted networks and benchmarks.
- **Panel:** an admin password is set on first run and stored as an argon2id hash. By default the panel listens on `127.0.0.1` only, on a separate address from the sync API, which can listen on any interface or interfaces.
- The panel uses session cookies (HttpOnly, SameSite=Strict) and CSRF tokens.
- Tokens are stored as hashes on the server. On the client, they are stored in `~/.config/fdsync/` with mode 0600.

## 8. Disk-usage budget

Conflicts and security features cost almost no disk space:

- Conflicts create no duplicate files (§5.4). Only "keep both" creates a file, and only when the admin explicitly chooses it.
- **Trash** (a convenience, not a backup):
  - Applies only to files that sync overwrote or deleted.
  - **Hard cap: 50 MB per side, for all folders together.** Settings can lower it or turn it off (0), never raise it. The cap holds after every write: each file put in evicts the oldest entries immediately.
  - Files over 16 MB are never kept. Entries older than 7 days are dropped.
  - Location: `trash/` in the data directory, never in a synced folder. If the data directory is on another drive than a folder, files are copied there instead of moved.
- Index databases take about 200 bytes per file plus block hashes, so about 50 MB for 100k files with a typical size mix.
- Logs rotate: 2 × 5 MB.
- An audit log of admin actions (resolutions, scope changes, deletes on one side) is capped at 5 MB.
- Certificates, tokens and configuration are a few KB.

## 9. Management panel

- **Dashboard:** the sync folders and connections, each with online/offline status, throughput and the number of conflicts.
- **Sync folders:** add a Windows folder, then edit its rules. Matched paths are previewed live as the rules are typed.
- **Connection view:** one page per client.
  - Bindings: list them, and create a new one by picking a local path on the client. The client lets the panel browse only under the roots it allows (default `$HOME`).
  - For each binding, a merged tree of the Windows side and the client side, with file counts for each side.
  - Every item is marked as one of: in scope / private (Windows) / private (WSL) / auto-excluded (with the reason) / in conflict / syncing.
  - Actions on an item: add to scope, cancel synchronization, delete on one side.
- **File browser:** the Windows tree and the client tree of one binding side by side, with the same status marks and right-click actions (add to scope, cancel synchronization, exclude, delete on one side, copy paths, locate in the other pane).
  - **File viewer:** open any file to see its contents: text with syntax highlighting, images, or a hex dump of the first 4 KiB for binary files.
  - **Diff in either direction:** WSL relative to Windows, or Windows relative to WSL. Text diffs are side by side or inline. Images compare side by side, by swipe, by onion skin or as a pixel difference.
  - Content is fetched on demand (`GET /api/v1/content?folder&connection&path&side`): the Windows side is read from disk, and the WSL side is streamed from the client. Neither is stored, and the WSL side is unavailable while the client is offline. Text is cut at 2 MiB.
- **Conflicts:** the queue and the diff view (§5.4), with the same two diff directions.
- **Settings:** listening addresses, TLS, password, trash limits, rescan interval.
- **Activity:** recent sync events and the audit log.

## 10. Phases

1. **Core:** index, scanner, watcher, one-way and then two-way sync of whole files between the server and one client, using plain rules. Plain HTTP and a static token.
2. **Correctness:** version vectors, tombstones, the conflict queue, rename detection, special-file exclusions, offline reconnect.
3. **Panel:** rule editor with live matching, the connection trees, conflict resolution with the diff view.
4. **Security:** TLS with pinning, the device-flow pairing, the panel login.
5. **Performance:** block-level transfer, parallel HTTP/2 streams, compression, a benchmark against `/mnt/c`.
6. **Multi-client:** several clients per folder, propagation through the hub, tombstone acknowledgment.
