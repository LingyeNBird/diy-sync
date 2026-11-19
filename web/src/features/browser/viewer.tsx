// File viewer: one side's contents, or a diff of one side relative to the other.
import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import {
  ArrowDown,
  ArrowLeftRight,
  ArrowUp,
  ClipboardCopy,
  Download,
  FileDiff,
  GitCompareArrows,
  Laptop,
  Loader2,
  Terminal,
  TriangleAlert,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { ago, formatBytes, timestamp } from '@/lib/format'
import { type FileContent, sidesDiffer } from '@/lib/content'
import type { AnnotatedNode } from '@/lib/status'
import type { Conflict, Connection, Side, SyncFolder } from '@/lib/types'
import { ContentFetchError, fetchContent } from '@/api/content'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { SegButton, Segmented } from '@/components/ui/segmented'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import {
  HexCompare,
  HexNav,
  HexView,
  ImageCompare,
  ImageView,
  imageModes,
  TextDiff,
  TextView,
  type DiffNav,
  type DiffStats,
  type ImageMode,
} from '@/components/file-view'
import { StatusBadge } from '@/components/status'
import { copyText } from './rows'

/** `view` shows one side; `diff` shows `other(base)` relative to `base`. */
export type ViewerMode = { view: Side } | { diff: Side }

const other = (s: Side): Side => (s === 'windows' ? 'wsl' : 'windows')

/** The mode a viewer opens in when started from a pane. */
export function defaultMode(node: AnnotatedNode, from: Side): ViewerMode {
  if (node.status === 'conflict' || sidesDiffer(node)) return { diff: other(from) } // what this side changed, relative to the other
  return { view: (from === 'windows' ? node.win : node.wsl) ? from : other(from) }
}


export function FileViewer({
  node,
  conn,
  folder,
  conflict,
  initial,
  onClose,
}: {
  node: AnnotatedNode
  conn: Connection
  folder: SyncFolder
  conflict?: Conflict
  initial: ViewerMode
  onClose: () => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [mode, setMode] = useState<ViewerMode>(() => {
    // A diff needs both sides (an edit-delete conflict has only one).
    const has = (s: Side) => {
      const v = conflict && (s === 'windows' ? conflict.win : conflict.wsl)
      return v ? !v.deleted : !!(s === 'windows' ? node.win : node.wsl)
    }
    if ('diff' in initial && !(has('windows') && has('wsl'))) return { view: has('windows') ? 'windows' : 'wsl' }
    return initial
  })
  const [inline, setInline] = useState(false)
  const [wrap, setWrap] = useState(false)
  const [hideUnchanged, setHideUnchanged] = useState(false)
  const [imageMode, setImageMode] = useState<ImageMode>('side')
  const [fit, setFit] = useState(true)
  const [hexOffset, setHexOffset] = useState(0)
  const [stats, setStats] = useState<DiffStats>()
  const nav = useRef<DiffNav>(null)

  const sideName = (s: Side) => (s === 'windows' ? t('common.windows') : conn.name)
  // A conflict carries the versions the decision is about; they win over the tree's metadata.
  const meta = (s: Side) => {
    const v = conflict && (s === 'windows' ? conflict.win : conflict.wsl)
    if (v) return v.deleted ? undefined : v
    return s === 'windows' ? node.win : node.wsl
  }
  const both = !!meta('windows') && !!meta('wsl')
  const differ = !!conflict || sidesDiffer(node)
  const hash = (s: Side) =>
    conflict ? (s === 'windows' ? conflict.win.hash : conflict.wsl.hash) : undefined // the tree carries no hashes

  // node and conflict are identified by path and id in the key.
  // eslint-disable-next-line @tanstack/query/exhaustive-deps
  const query = (side: Side) => ({
    queryKey: ['content', folder.id, conn.id, node.path, side, conflict?.id ?? '', conn.online, hexOffset],
    queryFn: () => fetchContent({ folderId: folder.id, connectionId: conn.id, path: node.path, side, offset: hexOffset }),
    placeholderData: (prev: FileContent | undefined) => prev, // keep the page while the next one loads
    enabled: !!meta(side),
    retry: false,
    staleTime: 30_000,
  })
  const win = useQuery(query('windows'))
  const wsl = useQuery(query('wsl'))
  const res = (s: Side) => (s === 'windows' ? win : wsl)

  const isDiff = 'diff' in mode
  const base: Side = isDiff ? mode.diff : mode.view
  const cmp = other(base)
  const needed: Side[] = isDiff ? [base, cmp] : [mode.view]
  const loading = needed.some((s) => res(s).isLoading)
  const error = needed.map((s) => ({ s, e: res(s).error })).find((x) => x.e)
  const content = (s: Side) => res(s).data as FileContent | undefined
  const shown = content(isDiff ? cmp : mode.view)
  const kind = content(base)?.type ?? shown?.type

  const setDiff = (b: Side) => {
    setStats(undefined)
    setMode({ diff: b })
  }

  const copyContents = () => {
    const c = shown
    if (c?.type === 'text') void copyText(c.text).then(() => toast.success(t('common.copied')))
  }
  const download = () => {
    const c = shown
    if (!c) return
    const a = document.createElement('a')
    a.download = node.name
    if (c.type === 'text') a.href = URL.createObjectURL(new Blob([c.text], { type: 'text/plain' }))
    else if (c.type === 'image') a.href = c.src
    else a.href = URL.createObjectURL(new Blob([c.bytes as BlobPart]))
    a.click()
    setTimeout(() => a.href.startsWith('blob:') && URL.revokeObjectURL(a.href), 1000)
  }

  const errText = (s: Side, e: unknown) => {
    const code = e instanceof ContentFetchError ? e.code : 'unreadable'
    return t(`viewer.errors.${code}`, { name: sideName(s) })
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className='flex h-[88vh] max-h-[88vh] w-[96vw] max-w-[96vw] flex-col gap-3 p-4 sm:max-w-[min(96vw,1600px)]'>
        <DialogHeader className='gap-1 pe-8'>
          <DialogTitle className='flex items-center gap-2 font-mono text-sm font-medium break-all'>
            {node.path}
            <StatusBadge status={node.status} />
          </DialogTitle>
          <DialogDescription className='flex flex-wrap gap-x-5 gap-y-1 text-xs'>
            {(['windows', 'wsl'] as Side[]).map((s) => {
              const m = meta(s)
              const Icon = s === 'windows' ? Laptop : Terminal
              return (
                <span key={s} className='flex items-center gap-1.5 tabular-nums'>
                  <Icon className='size-3.5' />
                  <span className='font-medium text-foreground'>{sideName(s)}</span>
                  {m ? (
                    <>
                      {formatBytes(m.size)} · <span title={timestamp(m.mtime)}>{ago(m.mtime)}</span>
                      {hash(s) && (
                        <>
                          {' · '}
                          <span className={cn('font-mono', differ && s === 'wsl' && 'text-amber-600 dark:text-amber-400')}>{hash(s)}</span>
                        </>
                      )}
                    </>
                  ) : (
                    <span className='italic'>{t('viewer.notOnSide')}</span>
                  )}
                </span>
              )
            })}
          </DialogDescription>
        </DialogHeader>

        {/* Toolbar */}
        <div className='flex flex-wrap items-center gap-2'>
          <Segmented>
            {(['windows', 'wsl'] as Side[]).map((s) => (
              <SegButton key={s} on={!isDiff && mode.view === s} disabled={!meta(s)} onClick={() => setMode({ view: s })}>
                {s === 'windows' ? <Laptop /> : <Terminal />} {sideName(s)}
              </SegButton>
            ))}
            <SegButton on={isDiff} disabled={!both} onClick={() => setDiff(isDiff ? mode.diff : 'windows')}>
              <FileDiff /> {t('viewer.diff')}
            </SegButton>
          </Segmented>

          {isDiff && (
            <>
              <Segmented>
                <SegButton on={mode.diff === 'windows'} onClick={() => setDiff('windows')}>
                  {t('viewer.relative', { a: sideName('wsl'), b: sideName('windows') })}
                </SegButton>
                <SegButton on={mode.diff === 'wsl'} onClick={() => setDiff('wsl')}>
                  {t('viewer.relative', { a: sideName('windows'), b: sideName('wsl') })}
                </SegButton>
              </Segmented>
              <IconButton label={t('viewer.swap')} onClick={() => setDiff(cmp)}>
                <ArrowLeftRight />
              </IconButton>
            </>
          )}

          {isDiff && kind === 'text' && (
            <>
              <Segmented>
                <SegButton on={!inline} onClick={() => setInline(false)}>
                  {t('viewer.sideBySide')}
                </SegButton>
                <SegButton on={inline} onClick={() => setInline(true)}>
                  {t('viewer.inline')}
                </SegButton>
              </Segmented>
              <IconButton label={t('viewer.prevChange')} onClick={() => nav.current?.go('previous')} disabled={!stats?.changes}>
                <ArrowUp />
              </IconButton>
              <IconButton label={t('viewer.nextChange')} onClick={() => nav.current?.go('next')} disabled={!stats?.changes}>
                <ArrowDown />
              </IconButton>
              <Toggle id='hide' checked={hideUnchanged} onChange={setHideUnchanged} label={t('viewer.hideUnchanged')} />
            </>
          )}
          {isDiff && kind === 'image' && (
            <Segmented>
              {imageModes.map((m) => (
                <SegButton key={m} on={imageMode === m} onClick={() => setImageMode(m)}>
                  {t(`viewer.image.${m}`)}
                </SegButton>
              ))}
            </Segmented>
          )}
          {!isDiff && kind === 'image' && (
            <Segmented>
              <SegButton on={fit} onClick={() => setFit(true)}>
                {t('viewer.image.fit')}
              </SegButton>
              <SegButton on={!fit} onClick={() => setFit(false)}>
                {t('viewer.image.actual')}
              </SegButton>
            </Segmented>
          )}
          {kind === 'text' && <Toggle id='wrap' checked={wrap} onChange={setWrap} label={t('viewer.wrap')} />}
          {kind === 'binary' && (
            <HexNav offset={hexOffset} size={Math.max(meta('windows')?.size ?? 0, meta('wsl')?.size ?? 0)} onChange={setHexOffset} />
          )}

          <div className='ms-auto flex items-center gap-1'>
            {conflict && (
              <Button size='sm' className='h-8' onClick={() => navigate({ to: '/conflicts', search: { path: node.path } })}>
                <GitCompareArrows /> {t('actions.resolveConflict')}
              </Button>
            )}
            {!isDiff && shown?.type === 'text' && (
              <IconButton label={t('viewer.copy')} onClick={copyContents}>
                <ClipboardCopy />
              </IconButton>
            )}
            {!isDiff && shown && (
              <IconButton label={t('viewer.download', { side: sideName(mode.view) })} onClick={download}>
                <Download />
              </IconButton>
            )}
          </div>
        </div>

        {/* Column captions for a diff: base on the left, compared side on the right. */}
        {isDiff && !loading && !error && kind === 'text' && !inline && (
          <div className='-mb-3 grid grid-cols-2 rounded-t-lg border border-b-0 bg-muted/40 text-xs text-muted-foreground'>
            <Caption role={t('viewer.base')} side={sideName(base)} />
            <Caption role={t('viewer.compared')} side={sideName(cmp)} className='border-s' />
          </div>
        )}

        <div className={cn('relative min-h-0 flex-1 overflow-hidden border bg-card', isDiff && !loading && !error && kind === 'text' && !inline ? 'rounded-b-lg' : 'rounded-lg')}>
          {loading ? (
            <Center>
              <Loader2 className='size-5 animate-spin' /> {t('viewer.loading')}
            </Center>
          ) : error ? (
            <Center>
              <TriangleAlert className='size-5 text-amber-500' /> {errText(error.s, error.e)}
            </Center>
          ) : isDiff ? (
            <DiffBody
              id={`${folder.id}/${conn.id}/${base}/${node.path}`}
              a={content(base)}
              b={content(cmp)}
              aSize={meta(base)?.size ?? 0}
              bSize={meta(cmp)?.size ?? 0}
              aLabel={sideName(base)}
              bLabel={sideName(cmp)}
              inline={inline}
              wrap={wrap}
              hideUnchanged={hideUnchanged}
              imageMode={imageMode}
              hexOffset={hexOffset}
              onStats={setStats}
              navRef={nav}
            />
          ) : shown?.type === 'text' ? (
            <TextView text={shown.text} language={shown.language} wrap={wrap} modelPath={`view/${folder.id}/${conn.id}/${mode.view}/${node.path}`} />
          ) : shown?.type === 'image' ? (
            <ImageView src={shown.src} fit={fit} />
          ) : shown?.type === 'binary' ? (
            <HexView bytes={shown.bytes} size={meta(mode.view)?.size ?? 0} offset={shown.offset ?? 0} />
          ) : null}
        </div>

        <div className='flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground'>
          <span>
            {isDiff
              ? kind === 'text' && stats
                ? stats.changes === 0
                  ? t('viewer.identical')
                  : t('viewer.stats', { added: stats.added, removed: stats.removed, changes: stats.changes })
                : t('viewer.diffHint', { a: sideName(cmp), b: sideName(base) })
              : shown?.type === 'text'
                ? t('viewer.lines', { count: shown.text.split('\n').length - (shown.text.endsWith('\n') ? 1 : 0) })
                : null}
          </span>
          {(isDiff || mode.view === 'wsl') && <span>{t('viewer.onDemand', { name: conn.name })}</span>}
        </div>
      </DialogContent>
    </Dialog>
  )
}

export function DiffBody(p: {
  id: string
  a?: FileContent
  b?: FileContent
  aSize: number
  bSize: number
  aLabel: string
  bLabel: string
  inline: boolean
  wrap: boolean
  hideUnchanged: boolean
  imageMode: ImageMode
  hexOffset: number
  onStats: (s: DiffStats) => void
  navRef: React.RefObject<DiffNav | null>
}) {
  const { a, b } = p
  if (a?.type === 'text' && b?.type === 'text')
    return (
      <TextDiff
        key={p.id}
        id={p.id}
        original={a.text}
        modified={b.text}
        language={a.language ?? b.language}
        inline={p.inline}
        wrap={p.wrap}
        hideUnchanged={p.hideUnchanged}
        onStats={p.onStats}
        navRef={p.navRef}
      />
    )
  if (a?.type === 'image' && b?.type === 'image')
    return <ImageCompare base={a.src} other={b.src} baseLabel={p.aLabel} otherLabel={p.bLabel} mode={p.imageMode} />
  if (a?.type === 'binary' && b?.type === 'binary')
    return (
      <HexCompare
        base={a.bytes}
        other={b.bytes}
        baseSize={p.aSize}
        otherSize={p.bSize}
        baseLabel={p.aLabel}
        otherLabel={p.bLabel}
        offset={a.offset ?? p.hexOffset}
      />
    )
  return null
}

export function Caption({ role, side, className }: { role: string; side: string; className?: string }) {
  return (
    <div className={cn('px-4 py-1.5', className)}>
      <span className='font-medium text-foreground'>{side}</span> · {role}
    </div>
  )
}

export function Center({ children }: { children: React.ReactNode }) {
  return <div className='flex h-full items-center justify-center gap-2 p-6 text-center text-sm text-muted-foreground'>{children}</div>
}

export function IconButton({
  label,
  onClick,
  disabled,
  children,
}: {
  label: string
  onClick: () => void
  disabled?: boolean
  children: React.ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button variant='ghost' size='icon' className='size-8' onClick={onClick} disabled={disabled} aria-label={label}>
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

export function Toggle({ id, checked, onChange, label }: { id: string; checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <div className='flex items-center gap-1.5 px-1'>
      <Switch id={`viewer-${id}`} checked={checked} onCheckedChange={onChange} className='scale-90' />
      <Label htmlFor={`viewer-${id}`} className='text-xs font-normal'>
        {label}
      </Label>
    </div>
  )
}
