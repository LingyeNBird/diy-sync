import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { remember, remembered, usePageState } from '@/lib/page-state'
import { preloadEditor } from '@/components/code-editor'
import {
  Ban,
  ChevronsDownUp,
  ClipboardCopy,
  Code,
  Columns2,
  Crosshair,
  Eye,
  EyeOff,
  FileDiff,
  FolderOpen,
  GitCompareArrows,
  History,
  Info,
  Laptop,
  Link as LinkIcon,
  ListFilter,
  ListMinus,
  ListPlus,
  RefreshCw,
  Rows3,
  Search,
  Terminal,
  Trash2,
  Undo2,
  WifiOff,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import i18n from '@/i18n'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { ago } from '@/lib/format'
import type { AnnotatedNode } from '@/lib/status'
import type { Binding, Connection, Side, SyncFolder } from '@/lib/types'
import { api } from '@/api/client'
import { useData } from '@/api/data'
import { Button } from '@/components/ui/button'
import {
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuShortcut,
  ContextMenuSub,
  ContextMenuSubContent,
  ContextMenuSubTrigger,
} from '@/components/ui/context-menu'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { PageShell } from '@/components/page-shell'
import { OnlineDot } from '@/components/status'
import { findNode, useBindingTree } from '@/features/connections/detail'
import { can, ItemDetails, useItemActions, type ItemActions } from '@/features/connections/item-actions'
import { Pane } from './pane'
import { defaultMode, FileViewer, type ViewerMode } from './viewer'
import { ancestors, buildRows, copyText, onSide, windowsPath, wslPath, type Row } from './rows'

export function Browser({ connectionId, bindingId }: { connectionId?: string; bindingId?: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { connections, folders, bindings } = useData()
  // Opened without a choice (e.g. from the sidebar): the project shown last.
  const last = remembered<{ connection?: string; binding?: string }>('browser/last')
  const wantConn = connectionId ?? last?.connection
  const wantBinding = connectionId ? bindingId : (bindingId ?? last?.binding)
  const conn = connections.find((c) => c.id === wantConn) ?? connections[0]
  const bs = bindings.filter((b) => b.connectionId === conn?.id)
  const binding = bs.find((b) => b.id === wantBinding) ?? bs[0]
  const go = (connection: string, b?: string) => navigate({ to: '/browse', search: { connection, binding: b } })
  useEffect(() => {
    if (conn && binding) remember('browser/last', { connection: conn.id, binding: binding.id })
  }, [conn, binding])
  useEffect(preloadEditor, []) // the viewer needs it; fetch it once the page is idle

  if (!conn)
    return (
      <PageShell fixed fluid title={t('browser.title')} description={t('browser.description')} breadcrumb={`${t('nav.sync')} / ${t('nav.browser')}`}>
        <div className='flex flex-1 flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-8 text-center text-muted-foreground'>
          <div className='font-medium text-foreground'>{t('browser.noConnections')}</div>
          <p className='max-w-md text-sm'>{t('browser.noConnectionsHint')}</p>
        </div>
      </PageShell>
    )

  return (
    <PageShell
      fixed
      fluid
      title={t('browser.title')}
      description={t('browser.description')}
      breadcrumb={`${t('nav.sync')} / ${t('nav.browser')}`}
      actions={
        <>
          <Select value={conn.id} onValueChange={(v) => go(v)}>
            <SelectTrigger className='h-8 w-52' aria-label={t('browser.connection')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {connections.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  <OnlineDot online={c.online} /> {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {binding && (
            <Select value={binding.id} onValueChange={(v) => go(conn.id, v)}>
              <SelectTrigger className='h-8 w-72' aria-label={t('browser.binding')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {bs.map((b) => (
                  <SelectItem key={b.id} value={b.id}>
                    {folders.find((f) => f.id === b.folderId)?.name}
                    <span className='font-mono text-xs text-muted-foreground'>↔ {b.localPath}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </>
      }
    >
      {binding ? (
        <BrowserView
          key={binding.id}
          conn={conn}
          binding={binding}
          folder={folders.find((f) => f.id === binding.folderId)!}
        />
      ) : (
        <div className='flex flex-1 items-center justify-center rounded-lg border border-dashed text-muted-foreground'>
          {t('browser.noBindings')}
        </div>
      )}
    </PageShell>
  )
}

type Sel = { paths: Set<string>; anchor?: number; focus?: string }
const emptySel = (): Sel => ({ paths: new Set() })
const other = (s: Side): Side => (s === 'windows' ? 'wsl' : 'windows')

function BrowserView({ conn, binding, folder }: { conn: Connection; binding: Binding; folder: SyncFolder }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { conflicts } = useData()
  const { nodes, totals, loading } = useBindingTree(binding, folder, conn)
  const { actions, dialog } = useItemActions(folder, conn)

  // Remembered per binding: each project comes back as it was left.
  const k = (name: string) => `browser/${binding.id}/${name}`
  const [query, setQuery] = usePageState(k('query'), '')
  const [onlyDiff, setOnlyDiff] = usePageState(k('onlyDiff'), false)
  const [align, setAlign] = usePageState(k('align'), false)
  const [link, setLink] = usePageState(k('link'), true)
  const [active, setActive] = usePageState<Side>(k('active'), 'windows')
  const [expanded, setExpanded] = usePageState<Record<Side, Set<string>>>(k('expanded'), () => ({ windows: new Set(), wsl: new Set() }))
  const [sel, setSel] = usePageState<Record<Side, Sel>>(k('sel'), () => ({ windows: emptySel(), wsl: emptySel() }))
  const [reveal, setReveal] = useState<Partial<Record<Side, { path: string; nonce: number }>>>({})
  const [scrollTop, setScrollTop] = useState<number>()
  const [props, setProps] = useState<string>()
  const [viewer, setViewer] = usePageState<{ path: string; mode: ViewerMode } | undefined>(k('viewer'), undefined)

  // Hidden from this browser (a view setting kept on the server), and whether each side shows them anyway.
  const [showHidden, setShowHidden] = usePageState<Record<Side, boolean>>(k('showHidden'), () => ({ windows: false, wsl: false }))
  const winHidden = folder.hidden.windows
  const wslHidden = folder.hidden[`wsl:${conn.id}`]
  const hidden = useMemo(() => ({ windows: new Set(winHidden ?? []), wsl: new Set(wslHidden ?? []) }), [winHidden, wslHidden])
  const { setHidden } = useData()
  const hide = (side: Side, nodes: AnnotatedNode[], on: boolean) => {
    if (!nodes.length) return
    void setHidden(folder.id, side, conn.id, nodes.map((n) => n.path), on).then(() => {
      if (on) setSel((s) => ({ ...s, [side]: emptySel() }))
      toast.success(
        on
          ? t('browser.hiddenToast', { count: nodes.length, side: side === 'windows' ? t('common.windows') : conn.name })
          : t('browser.unhiddenToast', { count: nodes.length })
      )
    })
  }

  // In compare layout both panes always share expansion so rows line up.
  const linked = link || align
  const rows = useMemo(() => {
    const opts = { query, onlyDiff, align, hidden, showHidden }
    const winExp = align ? expanded.windows : expanded.windows
    const wslExp = align ? expanded.windows : expanded.wsl
    return {
      windows: buildRows(nodes, { ...opts, side: 'windows', expanded: winExp }),
      wsl: buildRows(nodes, { ...opts, side: 'wsl', expanded: wslExp }),
    }
  }, [nodes, expanded, query, onlyDiff, align, hidden, showHidden])

  const toggle = useCallback(
    (side: Side, path: string, force?: boolean) =>
      setExpanded((e) => {
        const apply = (s: Set<string>) => {
          const n = new Set(s)
          const open = force ?? !n.has(path)
          if (open) n.add(path)
          else n.delete(path)
          return n
        }
        return linked ? { windows: apply(e.windows), wsl: apply(e.wsl) } : { ...e, [side]: apply(e[side]) }
      }),
    [linked, setExpanded]
  )

  /** Makes a path visible (and selected) in a pane: opens its folders, scrolls to it. */
  const revealIn = (side: Side, path: string) => {
    setExpanded((e) => {
      const n = new Set(e[side])
      ancestors(path).forEach((a) => n.add(a))
      return align ? { windows: n, wsl: n } : { ...e, [side]: n }
    })
    setSel((s) => ({ ...s, [side]: { paths: new Set([path]), focus: path } }))
    setReveal((r) => ({ ...r, [side]: { path, nonce: Date.now() } }))
  }

  const select = (side: Side, next: Sel) => {
    setSel((s) => {
      const res = { ...s, [side]: next }
      if (linked) {
        const o = other(side)
        const there = [...next.paths].filter((p) => {
          const n = findNode(nodes, p)
          return n && (align || onSide(n, o))
        })
        res[o] = { paths: new Set(there), focus: next.focus && there.includes(next.focus) ? next.focus : undefined }
      }
      return res
    })
    if (linked && next.focus) {
      const n = findNode(nodes, next.focus)
      if (n && onSide(n, other(side))) {
        setExpanded((e) => {
          const o = other(side)
          const s = new Set(e[o])
          ancestors(next.focus!).forEach((a) => s.add(a))
          return { ...e, [o]: s }
        })
        setReveal((r) => ({ ...r, [other(side)]: { path: next.focus!, nonce: Date.now() } }))
      }
    }
  }

  const onRowClick = (side: Side, e: React.MouseEvent, index: number) => {
    const list = rows[side]
    const row = list[index]
    if (row.ghost) return
    const cur = sel[side]
    if (e.shiftKey && cur.anchor !== undefined) {
      const [a, b] = [Math.min(cur.anchor, index), Math.max(cur.anchor, index)]
      const paths = new Set(list.slice(a, b + 1).filter((r) => !r.ghost).map((r) => r.node.path))
      select(side, { paths, anchor: cur.anchor, focus: row.node.path })
    } else if (e.ctrlKey || e.metaKey) {
      const paths = new Set(cur.paths)
      if (paths.has(row.node.path)) paths.delete(row.node.path)
      else paths.add(row.node.path)
      select(side, { paths, anchor: index, focus: row.node.path })
    } else {
      select(side, { paths: new Set([row.node.path]), anchor: index, focus: row.node.path })
    }
  }

  const onRowContext = (side: Side, index: number) => {
    setActive(side)
    const row = rows[side][index]
    if (row.ghost) return
    if (!sel[side].paths.has(row.node.path))
      select(side, { paths: new Set([row.node.path]), anchor: index, focus: row.node.path })
  }

  const targets = (side: Side) =>
    rows[side].filter((r) => !r.ghost && sel[side].paths.has(r.node.path)).map((r) => r.node)

  const onKey = (side: Side, e: React.KeyboardEvent) => {
    const list = rows[side]
    const real = list.map((r, i) => ({ r, i })).filter((x) => !x.r.ghost)
    if (!real.length) return
    const cur = sel[side].focus
    const found = real.findIndex((x) => x.r.node.path === cur)
    const pos = Math.max(0, found)
    // With nothing selected, only moving the selection does something.
    const focusRow = found >= 0 ? real[found].r : undefined
    const move = (to: number) => {
      const x = real[Math.min(real.length - 1, Math.max(0, to))]
      select(side, { paths: new Set([x.r.node.path]), anchor: x.i, focus: x.r.node.path })
      setReveal((r) => ({ ...r, [side]: { path: x.r.node.path, nonce: Date.now() } }))
    }
    if (e.key === 'ArrowDown') move(cur ? pos + 1 : 0)
    else if (e.key === 'ArrowUp') move(pos - 1)
    else if (e.key === 'Home') move(0)
    else if (e.key === 'End') move(real.length - 1)
    else if (e.key === 'ArrowRight' && focusRow?.node.kind === 'dir') {
      if (!focusRow.open) toggle(side, focusRow.node.path, true)
      else move(pos + 1)
    } else if (e.key === 'ArrowLeft' && focusRow) {
      if (focusRow.node.kind === 'dir' && focusRow.open) toggle(side, focusRow.node.path, false)
      else {
        const parent = ancestors(focusRow.node.path).pop()
        if (parent) move(real.findIndex((x) => x.r.node.path === parent))
      }
    } else if (e.key === 'Enter' && focusRow) {
      if (e.altKey) setProps(focusRow.node.path)
      else if (focusRow.node.kind === 'dir') toggle(side, focusRow.node.path)
      else open(side, focusRow.node)
    } else if (e.key === 'Delete') actions.requestDelete(targets(side), side)
    else if (e.key === 'Escape') select(side, emptySel())
    else if (e.key.toLowerCase() === 'a' && (e.ctrlKey || e.metaKey))
      select(side, { paths: new Set(real.map((x) => x.r.node.path)), focus: cur })
    else return
    e.preventDefault()
  }

  const propsNode = props ? findNode(nodes, props) : undefined
  const viewerNode = viewer ? findNode(nodes, viewer.path) : undefined
  const open = (side: Side, node: AnnotatedNode, mode?: ViewerMode) => {
    if (node.kind !== 'file') return
    setViewer({ path: node.path, mode: mode ?? defaultMode(node, side) })
  }
  const conflictFor = (path: string) =>
    conflicts.find((c) => c.path === path && c.connectionId === conn.id && c.folderId === folder.id)

  const menu = (side: Side) => (
    <PaneMenu
      side={side}
      targets={targets(side)}
      conn={conn}
      binding={binding}
      folder={folder}
      actions={actions}
      onToggle={(p) => toggle(side, p)}
      isOpen={(p) => rows[side].some((r) => r.node.path === p && r.open)}
      onLocate={(p) => {
        setActive(other(side))
        revealIn(other(side), p)
      }}
      onOpen={(n, mode) => open(side, n, mode)}
      onResolve={(p) => navigate({ to: '/conflicts', search: { path: p } })}
      onProps={setProps}
      isHidden={(n) => rows[side].some((r) => r.node.path === n.path && r.hidden)}
      onHide={(nodes, on) => hide(side, nodes, on)}
      onShowRule={() => navigate({ to: '/folders/$folderId', params: { folderId: folder.id } })}
    />
  )

  const pane = (side: Side) => (
    <Pane
      side={side}
      label={side === 'windows' ? t('common.windows') : conn.name}
      root={side === 'windows' ? folder.winPath : binding.localPath}
      total={side === 'windows' ? totals.win : totals.wsl}
      rows={rows[side]}
      selected={sel[side].paths}
      focusPath={sel[side].focus}
      active={active === side}
      reveal={reveal[side]}
      scrollTop={align ? scrollTop : undefined}
      onScroll={align ? setScrollTop : undefined}
      memoryKey={k(`scroll/${side}`)}
      loading={loading && nodes.length === 0}
      hiddenCount={hidden[side].size}
      showHidden={showHidden[side]}
      onToggleHidden={() => setShowHidden((h) => ({ ...h, [side]: !h[side] }))}
      onActivate={() => setActive(side)}
      onRowClick={(e, i) => onRowClick(side, e, i)}
      onRowContext={(i) => onRowContext(side, i)}
      onToggle={(p) => toggle(side, p)}
      onOpen={(i) => open(side, rows[side][i].node)}
      onKey={(e) => onKey(side, e)}
      menu={menu(side)}
    />
  )

  return (
    <div className='flex min-h-0 flex-1 flex-col gap-3'>
      {!conn.online && (
        <div className='flex items-center gap-2 rounded-md border border-dashed px-3 py-2 text-sm text-muted-foreground'>
          <WifiOff className='size-4' /> {t('browser.offline', { ago: ago(conn.lastSeen) })}
        </div>
      )}

      <div className='flex flex-wrap items-center gap-2'>
        <div className='relative w-full sm:w-72'>
          <Search className='absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground' />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t('browser.filter')} className='h-8 ps-8' />
        </div>
        <ToggleButton on={onlyDiff} onClick={() => setOnlyDiff((v) => !v)} icon={ListFilter} label={t('browser.onlyDiff')} hint={t('browser.onlyDiffHint')} />
        <ToggleButton on={align} onClick={() => setAlign((v) => !v)} icon={Rows3} label={t('browser.align')} hint={t('browser.alignHint')} />
        <ToggleButton on={linked} disabled={align} onClick={() => setLink((v) => !v)} icon={LinkIcon} label={t('browser.link')} hint={t('browser.linkHint')} />
        <div className='ms-auto flex gap-2'>
          <Button variant='ghost' size='sm' className='h-8' onClick={() => setExpanded({ windows: new Set(), wsl: new Set() })}>
            <ChevronsDownUp /> {t('browser.collapseAll')}
          </Button>
          <Button variant='ghost' size='sm' className='h-8' onClick={() => openInVSCode(folder.id, '')} title={folder.winPath}>
            <Code /> {t('browser.vscode')}
          </Button>
          <Button variant='ghost' size='sm' className='h-8' onClick={() => rescanFolder(folder.id, folder.name)}>
            <RefreshCw /> {t('browser.rescan')}
          </Button>
        </div>
      </div>

      <div className='flex min-h-0 flex-1 gap-3'>
        {pane('windows')}
        {pane('wsl')}
      </div>
      <div className='text-xs text-muted-foreground'>{t('browser.keysHint')}</div>

      <Sheet open={!!propsNode} onOpenChange={(o) => !o && setProps(undefined)}>
        <SheetContent className='overflow-y-auto sm:max-w-md'>
          <SheetHeader>
            <SheetTitle>{t('browser.menu.properties')}</SheetTitle>
          </SheetHeader>
          {propsNode && <ItemDetails node={propsNode} conn={conn} folder={folder} actions={actions} />}
        </SheetContent>
      </Sheet>

      {viewerNode && (
        <FileViewer
          key={viewerNode.path}
          node={viewerNode}
          conn={conn}
          folder={folder}
          conflict={conflictFor(viewerNode.path)}
          initial={viewer!.mode}
          onClose={() => setViewer(undefined)}
        />
      )}
      {dialog}
    </div>
  )
}

function ToggleButton({
  on,
  onClick,
  icon: Icon,
  label,
  hint,
  disabled,
}: {
  on: boolean
  onClick: () => void
  icon: React.ElementType
  label: string
  hint: string
  disabled?: boolean
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant='outline'
          size='sm'
          aria-pressed={on}
          disabled={disabled}
          onClick={onClick}
          className={cn('h-8', on && 'border-primary/40 bg-primary/10 text-foreground hover:bg-primary/15')}
        >
          <Icon /> {label}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{hint}</TooltipContent>
    </Tooltip>
  )
}

function PaneMenu({
  side,
  targets,
  conn,
  binding,
  folder,
  actions,
  onToggle,
  isOpen,
  onLocate,
  onOpen,
  onResolve,
  onProps,
  onShowRule,
  isHidden,
  onHide,
}: {
  side: Side
  targets: AnnotatedNode[]
  conn: Connection
  binding: Binding
  folder: SyncFolder
  actions: ItemActions
  onToggle: (path: string) => void
  isOpen: (path: string) => boolean
  onLocate: (path: string) => void
  onOpen: (node: AnnotatedNode, mode?: ViewerMode) => void
  onResolve: (path: string) => void
  onProps: (path: string) => void
  onShowRule: () => void
  isHidden: (n: AnnotatedNode) => boolean
  onHide: (nodes: AnnotatedNode[], hidden: boolean) => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  if (targets.length === 0) return <ContextMenuContent className='hidden' />
  const single = targets.length === 1 ? targets[0] : undefined
  const hereFull = (p: string) => (side === 'windows' ? windowsPath(folder.winPath, p) : wslPath(binding.localPath, p))
  const otherFull = (p: string) => (side === 'windows' ? wslPath(binding.localPath, p) : windowsPath(folder.winPath, p))
  const copy = (text: string) => void copyText(text).then(() => toast.success(t('common.copied'), { description: text }))
  const joined = (f: (p: string) => string) => targets.map((n) => f(n.path)).join('\n')
  const any = (f: (n: AnnotatedNode) => boolean) => targets.some(f)

  return (
    <ContextMenuContent className='w-64'>
      <ContextMenuLabel className='truncate text-xs font-normal text-muted-foreground'>
        {single ? single.path : t('browser.menu.selection', { count: targets.length })}
      </ContextMenuLabel>
      <ContextMenuSeparator />

      {single?.kind === 'dir' && (
        <ContextMenuItem onSelect={() => onToggle(single.path)}>
          <FolderOpen /> {isOpen(single.path) ? t('browser.menu.collapse') : t('browser.menu.expand')}
        </ContextMenuItem>
      )}
      {single?.kind === 'file' && (
        <ContextMenuItem onSelect={() => onOpen(single)} className='font-medium'>
          <Eye /> {t('browser.menu.open')}
          <ContextMenuShortcut>↵</ContextMenuShortcut>
        </ContextMenuItem>
      )}
      {single?.kind === 'file' && single.win && single.wsl && (
        <ContextMenuSub>
          <ContextMenuSubTrigger>
            <FileDiff /> {t('browser.menu.viewDiff')}
          </ContextMenuSubTrigger>
          <ContextMenuSubContent className='w-64'>
            {(['windows', 'wsl'] as Side[]).map((b) => (
              <ContextMenuItem key={b} onSelect={() => onOpen(single, { diff: b })}>
                {b === 'windows' ? <Terminal /> : <Laptop />}
                {t('viewer.relative', {
                  a: b === 'windows' ? conn.name : t('common.windows'),
                  b: b === 'windows' ? t('common.windows') : conn.name,
                })}
              </ContextMenuItem>
            ))}
          </ContextMenuSubContent>
        </ContextMenuSub>
      )}
      {single?.status === 'conflict' && (
        <ContextMenuItem onSelect={() => onResolve(single.path)}>
          <GitCompareArrows /> {t('actions.resolveConflict')}
        </ContextMenuItem>
      )}

      <ContextMenuSeparator />
      {any(can.addToScope) && (
        <ContextMenuItem onSelect={() => actions.addToScope(targets)}>
          <ListPlus /> {t('actions.addToScope')}
        </ContextMenuItem>
      )}
      {any(can.cancelSync) && (
        <ContextMenuItem onSelect={() => actions.cancelSync(targets)}>
          <ListMinus /> {t('actions.cancelSync')}
        </ContextMenuItem>
      )}
      {any(can.exclude) && (
        <ContextMenuItem onSelect={() => actions.exclude(targets)}>
          <Ban /> {t('actions.exclude')}
        </ContextMenuItem>
      )}
      {any((n) => n.status === 'excluded') && (
        <ContextMenuItem onSelect={() => actions.cancelExclusion(targets)} disabled={!any(can.cancelExclusion)}>
          <Undo2 /> {t('actions.cancelExclusion')}
        </ContextMenuItem>
      )}

      <ContextMenuSeparator />
      {single && onSide(single, other(side)) && (
        <ContextMenuItem onSelect={() => onLocate(single.path)}>
          <Crosshair /> {t('browser.menu.locate')}
        </ContextMenuItem>
      )}
      {single && (
        <ContextMenuItem onSelect={() => void navigate({ to: '/trash', search: { q: single.path, folder: folder.id } })}>
          <History /> {single.kind === 'dir' ? t('browser.menu.trashDir') : t('browser.menu.trashFile')}
        </ContextMenuItem>
      )}
      {single?.rule && (
        <ContextMenuItem onSelect={onShowRule}>
          <Columns2 /> {t('browser.menu.showRule')}
          <ContextMenuShortcut className='font-mono normal-case'>L{single.rule.index + 1}</ContextMenuShortcut>
        </ContextMenuItem>
      )}
      <ContextMenuSub>
        <ContextMenuSubTrigger>
          <ClipboardCopy /> {t('browser.menu.copy')}
        </ContextMenuSubTrigger>
        <ContextMenuSubContent className='w-56'>
          <ContextMenuItem onSelect={() => copy(joined((p) => p))}>{t('browser.menu.copyRelative')}</ContextMenuItem>
          <ContextMenuItem onSelect={() => copy(joined(hereFull))}>{t('browser.menu.copyHere')}</ContextMenuItem>
          <ContextMenuItem onSelect={() => copy(joined(otherFull))}>{t('browser.menu.copyOther')}</ContextMenuItem>
          {single && (
            <ContextMenuItem
              onSelect={() => {
                const dir = single.kind === 'dir' ? single.path : ancestors(single.path).pop() ?? ''
                copy(`cd '${wslPath(binding.localPath, dir).replace(/\/$/, '')}'`)
              }}
            >
              {t('browser.menu.copyCd')}
            </ContextMenuItem>
          )}
        </ContextMenuSubContent>
      </ContextMenuSub>
      {side === 'windows' && single && (
        <ContextMenuItem
          onSelect={() =>
            api('POST', '/api/reveal', { folder: folder.id, path: single.path }).catch((e: Error) =>
              toast.error(t('errors.actionFailed'), { description: e.message })
            )
          }
        >
          <FolderOpen /> {t('browser.menu.explorer')}
        </ContextMenuItem>
      )}
      {side === 'windows' && single && (
        <ContextMenuItem onSelect={() => openInVSCode(folder.id, single.path)}>
          <Code /> {t('browser.menu.vscode')}
        </ContextMenuItem>
      )}
      {single?.kind === 'dir' && (
        <ContextMenuItem onSelect={() => rescanFolder(folder.id, single.path)}>
          <RefreshCw /> {t('browser.menu.rescanFolder')}
        </ContextMenuItem>
      )}
      {any((n) => !isHidden(n)) && (
        <ContextMenuItem onSelect={() => onHide(targets.filter((n) => !isHidden(n)), true)}>
          <EyeOff /> {t('browser.menu.hide')}
        </ContextMenuItem>
      )}
      {any(isHidden) && (
        <ContextMenuItem onSelect={() => onHide(targets.filter(isHidden), false)}>
          <Eye /> {t('browser.menu.unhide')}
        </ContextMenuItem>
      )}
      {single && (
        <ContextMenuItem onSelect={() => onProps(single.path)}>
          <Info /> {t('browser.menu.properties')}
          <ContextMenuShortcut>Alt+↵</ContextMenuShortcut>
        </ContextMenuItem>
      )}

      <ContextMenuSeparator />
      <ContextMenuItem variant='destructive' onSelect={() => actions.requestDelete(targets, side)}>
        <Trash2 /> {t('actions.deleteHere')}
        <ContextMenuShortcut>Del</ContextMenuShortcut>
      </ContextMenuItem>
    </ContextMenuContent>
  )
}

export type { Row }

// openInVSCode opens a Windows folder or file (or, for '', the whole sync folder) in VS Code.
function openInVSCode(folderId: string, path: string) {
  api('POST', '/api/vscode', { folder: folderId, path }).catch((e: Error) =>
    toast.error(i18n.t('browser.vscodeFailed'), { description: e.message })
  )
}

// rescanFolder rescans the Windows folder at once and wakes the clients,
// which rescan their side in the same round.
function rescanFolder(folderId: string, label: string) {
  api('POST', `/api/folders/${encodeURIComponent(folderId)}/rescan`).then(
    () => toast.success(i18n.t('browser.rescanned', { path: label })),
    (e: Error) => toast.error(i18n.t('errors.actionFailed'), { description: e.message })
  )
}
