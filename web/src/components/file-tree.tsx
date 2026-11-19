import { useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import {
  ChevronRight,
  File,
  FileCode2,
  FileImage,
  FileText,
  Folder,
  FolderOpen,
  Link2,
  MoreHorizontal,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { ago, formatBytes, formatCount } from '@/lib/format'
import type { AnnotatedNode } from '@/lib/status'
import type { ItemStatus } from '@/lib/types'
import { StatusBadge } from '@/components/status'
import { Button } from '@/components/ui/button'

type Row = { node: AnnotatedNode; depth: number; open: boolean }

type FileTreeProps = {
  nodes: AnnotatedNode[]
  query: string
  statuses: Set<ItemStatus>
  selected?: string
  onSelect: (n: AnnotatedNode) => void
  renderActions?: (n: AnnotatedNode) => React.ReactNode
  wslLabel: string
}

const codeExt = /\.(go|ts|tsx|js|py|rs|sh|ps1|lua|json|ya?ml|toml)$/
const imgExt = /\.(png|jpe?g|gif|svg|ico|webp)$/
const textExt = /\.(md|txt|csv|ini|env)$/

function FileIcon({ node, open }: { node: AnnotatedNode; open: boolean }) {
  const cls = 'size-4 shrink-0'
  if (node.kind === 'symlink') return <Link2 className={cn(cls, 'text-rose-500')} />
  if (node.kind === 'dir')
    return open ? (
      <FolderOpen className={cn(cls, 'text-sky-500')} />
    ) : (
      <Folder className={cn(cls, 'fill-sky-500/15 text-sky-500')} />
    )
  if (codeExt.test(node.name)) return <FileCode2 className={cn(cls, 'text-muted-foreground')} />
  if (imgExt.test(node.name)) return <FileImage className={cn(cls, 'text-muted-foreground')} />
  if (textExt.test(node.name)) return <FileText className={cn(cls, 'text-muted-foreground')} />
  return <File className={cn(cls, 'text-muted-foreground')} />
}

const cols = 'grid-cols-[minmax(0,1fr)_8.5rem_9.5rem_9.5rem_2.25rem]'

export function FileTree({
  nodes,
  query,
  statuses,
  selected,
  onSelect,
  renderActions,
  wslLabel,
}: FileTreeProps) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  const parentRef = useRef<HTMLDivElement>(null)

  const filtering = query.trim() !== '' || statuses.size > 0

  // Which nodes are visible when a filter is active (matching files + their folders).
  const visible = useMemo(() => {
    if (!filtering) return undefined
    const q = query.trim().toLowerCase()
    const keep = new Set<string>()
    const walk = (n: AnnotatedNode): boolean => {
      let hit = false
      if (n.children) for (const c of n.children) hit = walk(c) || hit
      const self =
        (!q || n.path.toLowerCase().includes(q)) &&
        (statuses.size === 0 || statuses.has(n.status)) &&
        (n.kind !== 'dir' || !n.children?.length || statuses.size === 0)
      if (self || hit) keep.add(n.path)
      return self || hit
    }
    nodes.forEach(walk)
    return keep
  }, [nodes, query, statuses, filtering])

  const rows = useMemo(() => {
    const out: Row[] = []
    const push = (list: AnnotatedNode[], depth: number) => {
      for (const n of list) {
        if (visible && !visible.has(n.path)) continue
        const open = n.kind === 'dir' && (expanded.has(n.path) || (!!visible && !!query.trim()))
        out.push({ node: n, depth, open })
        if (open && n.children) push(n.children, depth + 1)
      }
    }
    push(nodes, 0)
    return out
  }, [nodes, expanded, visible, query])

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 34,
    overscan: 12,
  })

  const toggle = (path: string) =>
    setExpanded((s) => {
      const next = new Set(s)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })

  return (
    <div className='flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card'>
      <div
        className={cn(
          'grid shrink-0 items-center gap-3 border-b bg-muted/40 px-3 py-2 text-xs font-medium text-muted-foreground',
          cols
        )}
      >
        <span>{t('tree.name')}</span>
        <span>{t('tree.status')}</span>
        <span>{t('common.windows')}</span>
        <span className='truncate'>{wslLabel}</span>
        <span />
      </div>
      <div ref={parentRef} className='min-h-0 flex-1 overflow-auto'>
        {rows.length === 0 && (
          <div className='p-10 text-center text-sm text-muted-foreground'>
            {t('tree.nothing')}
          </div>
        )}
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((v) => {
            const { node, depth, open } = rows[v.index]
            const isDir = node.kind === 'dir'
            return (
              <div
                key={node.path}
                role='treeitem'
                aria-expanded={isDir ? open : undefined}
                aria-selected={selected === node.path}
                onClick={() => onSelect(node)}
                onDoubleClick={() => isDir && toggle(node.path)}
                className={cn(
                  'group absolute inset-x-0 grid cursor-default items-center gap-3 border-b border-border/50 px-3 text-sm hover:bg-muted/50',
                  cols,
                  selected === node.path && 'bg-accent hover:bg-accent'
                )}
                style={{ top: 0, height: v.size, transform: `translateY(${v.start}px)` }}
              >
                <div className='flex min-w-0 items-center gap-1.5' style={{ paddingInlineStart: depth * 18 }}>
                  {isDir ? (
                    <button
                      type='button'
                      className='-m-1 rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground'
                      onClick={(e) => {
                        e.stopPropagation()
                        toggle(node.path)
                      }}
                      aria-label={open ? t('tree.collapse') : t('tree.expand')}
                    >
                      <ChevronRight className={cn('size-3.5 transition-transform', open && 'rotate-90')} />
                    </button>
                  ) : (
                    <span className='w-3.5 shrink-0' />
                  )}
                  <FileIcon node={node} open={open} />
                  <span className='truncate'>{node.name}</span>
                  {isDir && (
                    <span className='ms-1 shrink-0 text-xs text-muted-foreground tabular-nums'>
                      {formatCount(Math.max(node.counts.win, node.counts.wsl))}
                    </span>
                  )}
                </div>
                <div>
                  <StatusBadge status={node.status} />
                </div>
                <SideCell meta={node.win} isDir={isDir} count={node.counts.win} />
                <SideCell meta={node.wsl} isDir={isDir} count={node.counts.wsl} />
                <div className='flex justify-end' onClick={(e) => e.stopPropagation()}>
                  {renderActions?.(node) ?? (
                    <Button variant='ghost' size='icon' className='size-7'>
                      <MoreHorizontal />
                    </Button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </div>
      <div className='shrink-0 border-t bg-muted/30 px-3 py-1.5 text-xs text-muted-foreground tabular-nums'>
        {t('tree.rowsShown', { count: rows.length })}{filtering && ` ${t('tree.filtered')}`}
      </div>
    </div>
  )
}

function SideCell({
  meta,
  isDir,
  count,
}: {
  meta?: { size: number; mtime: string }
  isDir: boolean
  count: number
}) {
  if (!meta) return <span className='text-xs text-muted-foreground/60'>—</span>
  if (isDir) return <DirCount count={count} />
  return (
    <span className='flex min-w-0 flex-col text-xs leading-tight tabular-nums'>
      <span>{formatBytes(meta.size)}</span>
      <span className='truncate text-muted-foreground'>{ago(meta.mtime)}</span>
    </span>
  )
}

function DirCount({ count }: { count: number }) {
  const { t } = useTranslation()
  return (
    <span className='text-xs text-muted-foreground tabular-nums'>
      {t('common.files', { count })}
    </span>
  )
}
