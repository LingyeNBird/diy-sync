import type { AnnotatedNode } from '@/lib/status'
import type { Side } from '@/lib/types'

export type Row = {
  node: AnnotatedNode
  depth: number
  open: boolean
  /** In compare layout: the item exists only on the other side (or is hidden here). */
  ghost: boolean
  /** Marked hidden on this side, and shown because hidden items are shown. */
  hidden?: boolean
}

export const onSide = (n: AnnotatedNode, side: Side) => (side === 'windows' ? !!n.win : !!n.wsl)

export type RowOptions = {
  side: Side
  expanded: Set<string>
  query: string
  onlyDiff: boolean
  align: boolean
  /** Paths hidden from the browser on each side (a folder hides its contents). */
  hidden?: Record<Side, Set<string>>
  /** Show each side's hidden items anyway (dimmed). */
  showHidden?: Record<Side, boolean>
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
  const otherSide: Side = o.side === 'windows' ? 'wsl' : 'windows'
  const marked = (side: Side, path: string) => !!o.hidden?.[side].has(path)
  // hidHere / hidThere: hidden (and not shown) on this pane's side / the
  // other side, inherited from a hidden folder.
  const push = (list: AnnotatedNode[], depth: number, inHere: boolean, inThere: boolean) => {
    for (const n of list) {
      const markHere = inHere || marked(o.side, n.path)
      const markThere = inThere || marked(otherSide, n.path)
      const hidHere = markHere && !o.showHidden?.[o.side]
      const hidThere = markThere && !o.showHidden?.[otherSide]
      let here = onSide(n, o.side) && !hidHere
      if (o.align) {
        // Both panes list the same rows: gone only where neither side shows it.
        const there = onSide(n, otherSide) && !hidThere
        if (!here && !there) continue
      } else if (!here) continue
      if (keep && !keep.has(n.path)) continue
      if (o.onlyDiff && n.status === 'synced') continue
      const open = n.kind === 'dir' && (o.expanded.has(n.path) || !!keep)
      if (hidHere) here = false
      out.push({ node: n, depth, open, ghost: !here, hidden: here && markHere })
      if (open && n.children) push(n.children, depth + 1, markHere, markThere)
    }
  }
  push(nodes, 0, false, false)
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
