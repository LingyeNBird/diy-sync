import type { AnnotatedNode } from '@/lib/status'
import type { Side } from '@/lib/types'

export type Row = {
  node: AnnotatedNode
  depth: number
  open: boolean
  /** In compare layout: the item exists only on the other side. */
  ghost: boolean
}

export const onSide = (n: AnnotatedNode, side: Side) => (side === 'windows' ? !!n.win : !!n.wsl)

export type RowOptions = {
  side: Side
  expanded: Set<string>
  query: string
  onlyDiff: boolean
  align: boolean
}

/** Flattens the merged tree into the visible rows of one pane. */
export function buildRows(nodes: AnnotatedNode[], o: RowOptions): Row[] {
  const q = o.query.trim().toLowerCase()

  // When filtering by text: matching items plus their folders, folders auto-open.
  let keep: Set<string> | undefined
  if (q) {
    keep = new Set()
    const walk = (n: AnnotatedNode): boolean => {
      let hit = false
      if (n.children) for (const c of n.children) hit = walk(c) || hit
      if (n.path.toLowerCase().includes(q) || hit) keep!.add(n.path)
      return n.path.toLowerCase().includes(q) || hit
    }
    nodes.forEach(walk)
  }

  const out: Row[] = []
  const push = (list: AnnotatedNode[], depth: number) => {
    for (const n of list) {
      const here = onSide(n, o.side)
      if (!here && !o.align) continue
      if (keep && !keep.has(n.path)) continue
      if (o.onlyDiff && n.status === 'synced') continue
      const open = n.kind === 'dir' && (o.expanded.has(n.path) || !!keep)
      out.push({ node: n, depth, open, ghost: !here })
      if (open && n.children) push(n.children, depth + 1)
    }
  }
  push(nodes, 0)
  return out
}

export const ancestors = (path: string) => {
  const parts = path.split('/')
  return parts.slice(0, -1).map((_, i) => parts.slice(0, i + 1).join('/'))
}

export function windowsPath(root: string, rel: string) {
  return `${root}\\${rel.replace(/\//g, '\\')}`
}

export function wslPath(root: string, rel: string) {
  return `${root}/${rel}`
}

export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Not a secure context (e.g. opened via a LAN IP): fall back to execCommand.
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  }
}
