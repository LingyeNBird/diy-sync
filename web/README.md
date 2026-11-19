# fdsync management panel

React + shadcn/ui, based on [shadcn-admin](https://github.com/satnaing/shadcn-admin) (MIT).
The production build (`dist/`) is embedded into `fdsync-server.exe` with `go:embed`.

```sh
pnpm install
pnpm dev      # http://localhost:5173
pnpm build    # → dist/
```

Status: **UI mockup**. Every screen runs on fake data from `src/mock/`
(`data.ts`, `conflicts.ts`, and the in-memory store `store.ts`). When the
server API exists, the pages switch to TanStack Query calls that return the
same types (`src/lib/types.ts`).

| Path | What |
|---|---|
| `src/lib/rules.ts` | Scope-rule matcher. The Go matcher must give the same results |
| `src/lib/status.ts` | Works out each tree item's status (synced / private / excluded …) |
| `src/components/file-tree.tsx` | Virtualized merged tree (Windows + WSL side) |
| `src/components/monaco.tsx` | Local Monaco setup (no CDN), rule highlighting, diff view |
| `src/features/*` | One folder per page |
| `src/i18n/` | i18next setup. `locales/en.ts` is the master list of strings; `locales/zh-CN.ts` must have the same keys (type-checked). Server messages arrive as codes + params and are translated with `tx()` |

## Adding a language

1. Copy `src/i18n/locales/zh-CN.ts` to `<code>.ts`, translate the values, keep the keys.
2. Register it in `src/i18n/index.ts` (`languages` and `resources`).
3. `pnpm build`. Missing or extra keys fail the type check.
