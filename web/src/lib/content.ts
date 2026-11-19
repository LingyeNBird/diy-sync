// File contents as the panel receives them for preview and diff.
// Real server: GET /api/v1/content?folder=&connection=&path=&side=
// (Windows side read from disk, WSL side streamed from the client on demand,
// never stored on the server).

export const TEXT_PREVIEW_LIMIT = 2 * 1024 * 1024 // larger text is cut here
export const BINARY_PREVIEW_BYTES = 4096 // hex view shows this much

export type FileContent =
  | { type: 'text'; text: string; language?: string; truncated?: boolean }
  | { type: 'image'; src: string; mime: string }
  | { type: 'binary'; bytes: Uint8Array; mime?: string; offset?: number; size?: number }

export type ContentError = 'offline' | 'missing' | 'unreadable'

const langByExt: Record<string, string> = {
  go: 'go',
  ts: 'typescript',
  tsx: 'typescript',
  mts: 'typescript',
  js: 'javascript',
  jsx: 'javascript',
  mjs: 'javascript',
  json: 'json',
  jsonl: 'json',
  ipynb: 'json',
  md: 'markdown',
  yaml: 'yaml',
  yml: 'yaml',
  py: 'python',
  sh: 'shell',
  bash: 'shell',
  ps1: 'powershell',
  rs: 'rust',
  ini: 'ini',
  toml: 'ini',
  env: 'ini',
  html: 'html',
  css: 'css',
}

const textExt = new Set([
  ...Object.keys(langByExt),
  'txt',
  'csv',
  'log',
  'mod',
  'sum',
  'local',
  'gitignore',
])
const textNames = new Set(['Makefile', 'Dockerfile', 'LICENSE', 'README'])
const imageExt: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  svg: 'image/svg+xml',
  webp: 'image/webp',
  ico: 'image/x-icon',
  bmp: 'image/bmp',
}

export const extOf = (name: string) => {
  const i = name.lastIndexOf('.')
  return i > 0 ? name.slice(i + 1).toLowerCase() : ''
}

export function languageFor(name: string): string | undefined {
  if (name === 'Dockerfile') return 'dockerfile'
  if (name === 'Makefile') return 'shell'
  if (name.startsWith('.bashrc')) return 'shell'
  return langByExt[extOf(name)]
}

/** What kind of preview a file gets, judged from its name (the server sniffs content too). */
export function contentKind(name: string): FileContent['type'] {
  const ext = extOf(name)
  if (imageExt[ext]) return 'image'
  if (textExt.has(ext) || textNames.has(name) || name.startsWith('.')) return 'text'
  return 'binary'
}

export const imageMime = (name: string) => imageExt[extOf(name)] ?? 'application/octet-stream'

/**
 * Do the two sides seem to hold different content? Judged from size and
 * modification time (the panel's tree has no hashes); clocks and file
 * systems round times differently, so up to a second apart counts as equal.
 */
export function sidesDiffer(n: { win?: { size: number; mtime: string }; wsl?: { size: number; mtime: string } }) {
  if (!n.win || !n.wsl) return false
  return n.win.size !== n.wsl.size || Math.abs(Date.parse(n.win.mtime) - Date.parse(n.wsl.mtime)) > 1000
}
