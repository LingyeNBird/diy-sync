import { useEffect, useLayoutEffect, useRef } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import {
  ChevronRight,
  Eye,
  EyeOff,
  File,
  FileCode2,
  FileImage,
  FileText,
  Folder,
  FolderOpen,
  Laptop,
  Link2,
  Loader2,
  Terminal,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { remember, remembered } from '@/lib/page-state'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { ago, formatBytes } from '@/lib/format'
import type { AnnotatedNode } from '@/lib/status'
import type { Side } from '@/lib/types'
import { ContextMenu, ContextMenuTrigger } from '@/components/ui/context-menu'
import { StatusBadge } from '@/components/status'
import type { Row } from './rows'

const codeExt = /\.(go|ts|tsx|js|py|rs|sh|ps1|lua|json|ya?ml|toml)$/
const imgExt = /\.(png|jpe?g|gif|svg|ico|webp)$/
const textExt = /\.(md|txt|csv|ini|env)$/

function Icon({ node, open }: { node: AnnotatedNode; open: boolean }) {
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

const cols = 'grid-cols-[minmax(0,1fr)_7.5rem_6.5rem_6.5rem]'

export type PaneProps = {
  side: Side
  label: string
  root: string
  total: number
  rows: Row[]
  selected: Set<string>
  focusPath?: string
  active: boolean
  reveal?: { path: string; nonce: number }
  scrollTop?: number
  onScroll?: (top: number) => void
  /** Where this pane's scroll position is remembered (page memory). */
  memoryKey?: string
  /** The tree is still being fetched or prepared. */
  loading?: boolean
  /** Items hidden on this side; showHidden lists them anyway (dimmed). */
  hiddenCount: number
  showHidden: boolean
  onToggleHidden: () => void
  onActivate: () => void
  onRowClick: (e: React.MouseEvent, index: number) => void
  onRowContext: (index: number) => void
  onToggle: (path: string) => void
  onOpen: (index: number) => void
  onKey: (e: React.KeyboardEvent) => void
  menu: React.ReactNode
}

export function Pane(p: PaneProps) {
  const { t } = useTranslation()
  const ref = useRef<HTMLDivElement>(null)
  const virtualizer = useVirtualizer({
    count: p.rows.length,
    getScrollElement: () => ref.current,
    estimateSize: () => 30,
    overscan: 16,
  })

  // Scroll a revealed row into view ("Locate in other pane", linked selection).
  // Each reveal is handled once: the row may only appear after its folders
  // open (rows change), but later row changes must not jump back to it.
  const handled = useRef<number>(undefined)
  useEffect(() => {
    if (!p.reveal || handled.current === p.reveal.nonce) return
    const i = p.rows.findIndex((r) => r.node.path === p.reveal!.path)
    if (i >= 0) {
      handled.current = p.reveal.nonce
      virtualizer.scrollToIndex(i, { align: 'auto' })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.reveal?.nonce, p.rows])

  // Back to the remembered scroll position once the rows are there.
  const restored = useRef<string>(undefined) // the memory key restored last (another binding: restore again)
  useLayoutEffect(() => {
    const el = ref.current
    if (restored.current === p.memoryKey || !el || !p.memoryKey || p.rows.length === 0) return
    restored.current = p.memoryKey
    const top = remembered<number>(p.memoryKey)
    if (top) {
      virtualizer.scrollToOffset(top)
      el.scrollTop = top
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.rows.length, p.memoryKey])

  // Synchronized scrolling in compare layout.
  useEffect(() => {
    const el = ref.current
    if (el && p.scrollTop !== undefined && Math.abs(el.scrollTop - p.scrollTop) > 1) el.scrollTop = p.scrollTop
  }, [p.scrollTop])

  const SideIcon = p.side === 'windows' ? Laptop : Terminal
  const real = p.rows.filter((r) => !r.ghost)
  const selNodes = real.filter((r) => p.selected.has(r.node.path)).map((r) => r.node)
  const selSize = selNodes.reduce((n, x) => n + ((p.side === 'windows' ? x.win?.size : x.wsl?.size) ?? 0), 0)

  return (
    <div
      className={cn(
        'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card transition-shadow',
        p.active && 'ring-2 ring-ring/40'
      )}
      onMouseDown={p.onActivate}
    >
      <div className='flex shrink-0 items-center gap-2 border-b px-3 py-2'>
        <SideIcon className='size-4 text-muted-foreground' />
        <span className='text-sm font-medium'>{p.label}</span>
        <span className='min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground'>{p.root}</span>
        <span className='shrink-0 text-xs text-muted-foreground tabular-nums'>
          {t('common.files', { count: p.total })}
        </span>
        {p.hiddenCount > 0 && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant={p.showHidden ? 'secondary' : 'ghost'}
                size='sm'
                className='-my-1 h-6 gap-1 px-1.5 text-xs'
                onClick={p.onToggleHidden}
                aria-pressed={p.showHidden}
              >
                {p.showHidden ? <Eye className='size-3.5' /> : <EyeOff className='size-3.5' />}
                {t('browser.hiddenCount', { count: p.hiddenCount })}
              </Button>
            </TooltipTrigger>
            <TooltipContent>{p.showHidden ? t('browser.hideHidden') : t('browser.showHidden')}</TooltipContent>
          </Tooltip>
        )}
      </div>
      <div
        className={cn(
          'grid shrink-0 items-center gap-3 border-b bg-muted/40 px-3 py-1.5 text-xs font-medium text-muted-foreground',
          cols
        )}
      >
        <span>{t('tree.name')}</span>
        <span>{t('tree.status')}</span>
        <span className='text-end'>{t('browser.colSize')}</span>
        <span>{t('browser.colModified')}</span>
      </div>

      {/* modal={false}: opening a dialog from the menu would otherwise leave
          the page with pointer-events: none (Radix lock conflict). */}
      <ContextMenu modal={false}>
        <ContextMenuTrigger asChild>
          <div
            ref={ref}
            tabIndex={0}
            role='tree'
            aria-multiselectable
            aria-label={p.label}
            onKeyDown={p.onKey}
            onFocus={p.onActivate}
            onScroll={(e) => {
              const top = e.currentTarget.scrollTop
              p.onScroll?.(top)
              if (p.memoryKey && restored.current === p.memoryKey) remember(p.memoryKey, top)
            }}
            className='min-h-0 flex-1 overflow-auto outline-none'
          >
            {p.rows.length === 0 &&
              (p.loading ? (
                <div className='flex items-center justify-center gap-2 p-10 text-sm text-muted-foreground'>
                  <Loader2 className='size-4 animate-spin' /> {t('browser.loadingTree')}
                </div>
              ) : (
                <div className='p-10 text-center text-sm text-muted-foreground'>{t('tree.nothing')}</div>
              ))}
            <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
              {virtualizer.getVirtualItems().map((v) => {
                const { node, depth, open, ghost, hidden } = p.rows[v.index]
                const isDir = node.kind === 'dir'
                const meta = p.side === 'windows' ? node.win : node.wsl
                const sel = p.selected.has(node.path) && !ghost
                return (
                  <div
                    key={node.path}
                    role='treeitem'
                    aria-selected={sel}
                    aria-expanded={isDir ? open : undefined}
                    onClick={(e) => p.onRowClick(e, v.index)}
                    onDoubleClick={() => !ghost && (isDir ? p.onToggle(node.path) : p.onOpen(v.index))}
                    onContextMenu={() => p.onRowContext(v.index)}
                    className={cn(
                      'group absolute inset-x-0 grid cursor-default items-center gap-3 px-3 text-sm select-none',
                      cols,
                      ghost
                        ? 'bg-[repeating-linear-gradient(135deg,transparent_0_6px,var(--muted)_6px_7px)] text-muted-foreground/50'
                        : sel
                          ? p.active
                            ? 'bg-primary/10 text-foreground'
                            : 'bg-muted'
                          : 'hover:bg-muted/50',
                      p.focusPath === node.path && p.active && !ghost && 'outline outline-1 -outline-offset-1 outline-ring/60'
                    )}
                    style={{ top: 0, height: v.size, transform: `translateY(${v.start}px)` }}
                  >
                    <div className='flex min-w-0 items-center gap-1.5' style={{ paddingInlineStart: depth * 16 }}>
                      {isDir ? (
                        <button
                          type='button'
                          tabIndex={-1}
                          className='-m-1 rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground'
                          onClick={(e) => {
                            e.stopPropagation()
                            p.onToggle(node.path)
                          }}
                          aria-label={open ? t('tree.collapse') : t('tree.expand')}
                        >
                          <ChevronRight className={cn('size-3.5 transition-transform', open && 'rotate-90')} />
                        </button>
                      ) : (
                        <span className='w-3.5 shrink-0' />
                      )}
                      <span className={cn(ghost && 'opacity-40')}>
                        <Icon node={node} open={open} />
                      </span>
                      <span className={cn('truncate', ghost && 'italic', hidden && 'opacity-50')}>{node.name}</span>
                      {hidden && <EyeOff className='size-3 shrink-0 text-muted-foreground' aria-label={t('browser.hiddenMark')} />}
                      {ghost && <span className='shrink-0 text-[11px]'>· {t('browser.missingHere')}</span>}
                    </div>
                    <div>{!ghost && <StatusBadge status={node.status} />}</div>
                    <span className='text-end text-xs text-muted-foreground tabular-nums'>
                      {!ghost &&
                        (isDir
                          ? t('common.files', { count: p.side === 'windows' ? node.counts.win : node.counts.wsl })
                          : meta && formatBytes(meta.size))}
                    </span>
                    <span className='truncate text-xs text-muted-foreground'>{!ghost && meta && !isDir && ago(meta.mtime)}</span>
                  </div>
                )
              })}
            </div>
          </div>
        </ContextMenuTrigger>
        {p.menu}
      </ContextMenu>

      <div className='flex shrink-0 justify-between gap-3 border-t bg-muted/30 px-3 py-1.5 text-xs text-muted-foreground tabular-nums'>
        <span>{t('browser.paneItems', { count: real.length })}</span>
        {selNodes.length > 0 && (
          <span>{t('browser.paneSelected', { count: selNodes.length, size: formatBytes(selSize) })}</span>
        )}
      </div>
    </div>
  )
}
