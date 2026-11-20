// Shared panel data types. The mockup fills these from src/mock;
// the real panel will get the same shapes from the server API.

export type Side = 'windows' | 'wsl'

/** Status of one file or folder in a binding's merged tree. */
export type ItemStatus =
  | 'synced' // in scope, both sides identical
  | 'syncing' // in scope, transfer pending or running
  | 'conflict' // in scope, waiting for an admin decision
  | 'excluded' // auto-excluded (symlink, invalid name, case collision…)
  | 'private-win' // outside scope, exists only on Windows
  | 'private-wsl' // outside scope, exists only on this WSL client
  | 'private-both' // outside scope, each side has its own copy
  | 'partial' // folder: some descendants in scope, some not

export type ExclusionReason =
  | 'symlink'
  | 'junction'
  | 'invalid-name'
  | 'reserved-name'
  | 'case-collision'
  | 'special-file'
  | 'manual' // excluded by the admin from the panel

export type AutoExclusion = {
  path: string
  reason: ExclusionReason
  /** Values for the reason's message, e.g. { target: '../../shared' }. */
  params?: Record<string, string>
  origin: string // connection name or 'Windows'
  since: string // ISO time
}

export type SyncFolder = {
  id: string
  name: string
  winPath: string
  rules: string[]
  autoExcluded: AutoExclusion[]
  fileCount: number
  totalBytes: number
  state: 'idle' | 'syncing' | 'conflict' | 'paused'
  conflictPolicy: 'ask' | 'newest-wins'
  /** Paused by itself (state is also 'paused' while everything is paused). */
  paused: boolean
  /** Not shown in the file browser (view only), by side: "windows" or "wsl:<connection>". */
  hidden: Record<string, string[]>
}

export type Binding = {
  id: string
  folderId: string
  connectionId: string
  localPath: string
  state: 'idle' | 'syncing' | 'conflict' | 'offline'
  winCount: number
  wslCount: number
  inScopeCount: number
  conflicts: number
  lastSync: string
}

export type Connection = {
  id: string
  name: string
  distro: string
  address: string
  online: boolean
  lastSeen: string
  version: string
  networkMode: 'NAT' | 'mirrored' | 'bridged'
  pairedAt: string
  upBps: number
  downBps: number
  paired?: boolean
  hostname?: string // what the client reports; name is the alias shown
  warnings?: string[] // e.g. 'watch-limit'
}

export type PairingRequest = {
  code: string
  hostname: string
  distro: string
  address: string
  requestedAt: string
  expiresAt: string
}

export type TreeNode = {
  name: string
  path: string
  kind: 'dir' | 'file' | 'symlink'
  win?: { size: number; mtime: string }
  wsl?: { size: number; mtime: string }
  children?: TreeNode[]
}

export type ConflictKind = 'concurrent-edit' | 'edit-delete' | 'scope-add'

export type ConflictVersion = {
  size: number
  mtime: string
  hash: string
  deleted?: boolean
}

export type Conflict = {
  id: string
  folderId: string
  connectionId: string
  path: string
  kind: ConflictKind
  content: 'text' | 'image' | 'binary'
  language?: string
  win: ConflictVersion & { text?: string; image?: string }
  wsl: ConflictVersion & { text?: string; image?: string }
  detectedAt: string
}

export type ActivityEvent = {
  id: string
  time: string
  level: 'info' | 'warn' | 'error'
  kind:
    | 'sync'
    | 'delete'
    | 'rename'
    | 'conflict'
    | 'resolve'
    | 'scope'
    | 'exclude'
    | 'connect'
    | 'disconnect'
    | 'pair'
    | 'restore'
  folderId?: string
  connectionId?: string
  path?: string
  /** Message code, translated by the panel (activity.events.<code>). */
  code: string
  params?: Record<string, string | number>
  admin?: boolean // part of the audit log
}

/** One kept version in a side's trash (GET /api/trash). */
export type TrashItem = {
  side: Side
  connectionId?: string
  folderId: string
  id: string
  path: string
  when: string
  size: number
}
