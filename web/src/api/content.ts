// File contents for preview and diff: GET /api/content (the WSL side is read
// by the client on request and never stored on Windows).
import {
  BINARY_PREVIEW_BYTES,
  contentKind,
  imageMime,
  languageFor,
  TEXT_PREVIEW_LIMIT,
  type ContentError,
  type FileContent,
} from '@/lib/content'
import type { Side } from '@/lib/types'
import { ApiError, apiBytes } from './client'

export class ContentFetchError extends Error {
  constructor(public code: ContentError) {
    super(code)
  }
}

const IMAGE_LIMIT = 16 << 20
const knownBinary =
  /\.(exe|dll|so|o|a|lib|bin|iso|img|zip|gz|tgz|xz|zst|7z|rar|jar|class|pyc|parquet|arrow|feather|npy|npz|pt|onnx|db|sqlite|xlsx?|docx?|pptx?|pdf|woff2?|ttf|otf|mp3|mp4|mkv|mov|wav|flac|ogg|webm)$/i

function looksBinary(b: Uint8Array) {
  const n = Math.min(b.length, 8000)
  for (let i = 0; i < n; i++) if (b[i] === 0) return true
  return false
}

export async function fetchContent(opts: {
  folderId: string
  connectionId: string
  path: string
  side: Side
  offset?: number // binary files: where the hex view starts
  trash?: string // a kept version from that side's trash instead of the file
}): Promise<FileContent> {
  const name = opts.path.split('/').pop() ?? opts.path
  // Unknown extensions are tried as text; NUL bytes turn them into binary.
  let kind: FileContent['type'] = contentKind(name) === 'image' ? 'image' : knownBinary.test(name) ? 'binary' : 'text'
  const limit = kind === 'text' ? TEXT_PREVIEW_LIMIT : kind === 'image' ? IMAGE_LIMIT : BINARY_PREVIEW_BYTES
  const q = new URLSearchParams({
    folder: opts.folderId,
    connection: opts.connectionId,
    side: opts.side,
    path: opts.path,
    limit: String(limit),
  })
  if (kind === 'binary' && opts.offset) q.set('offset', String(opts.offset))
  if (opts.trash) q.set('trash', opts.trash)
  let res: { bytes: Uint8Array; size: number }
  try {
    res = await apiBytes(`/api/content?${q}`)
  } catch (e) {
    const status = e instanceof ApiError ? e.status : 0
    throw new ContentFetchError(status === 504 ? 'offline' : status === 404 ? 'missing' : 'unreadable')
  }
  if (kind === 'text' && looksBinary(res.bytes)) kind = 'binary'
  if (kind === 'text')
    return {
      type: 'text',
      text: new TextDecoder('utf-8').decode(res.bytes),
      language: languageFor(name),
      truncated: res.size > res.bytes.length,
    }
  if (kind === 'image')
    return { type: 'image', src: URL.createObjectURL(new Blob([res.bytes as BlobPart], { type: imageMime(name) })), mime: imageMime(name) }
  return { type: 'binary', bytes: res.bytes.subarray(0, BINARY_PREVIEW_BYTES), offset: opts.offset ?? 0, size: res.size }
}
