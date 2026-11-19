import { useMemo, useState } from 'react'
import { ArrowLeftRight, CheckCircle2, Copy, Loader2, TriangleAlert, FileWarning, Laptop, Terminal, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { ago, formatBytes, timestamp } from '@/lib/format'
import type { Conflict, ConflictVersion, Side } from '@/lib/types'
import { useQuery } from '@tanstack/react-query'
import { ContentFetchError, fetchContent } from '@/api/content'
import { useData, type Resolution } from '@/api/data'
import type { FileContent } from '@/lib/content'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { HexCompare, HexNav, ImageCompare, imageModes, TextDiff, type ImageMode } from '@/components/file-view'
import { SegButton, Segmented } from '@/components/ui/segmented'
import { PageShell } from '@/components/page-shell'

export function Conflicts({ initialPath }: { initialPath?: string }) {
  const { t } = useTranslation()
  const { conflicts, connections, folders, resolve } = useData()
  const [activeId, setActiveId] = useState<string | undefined>(
    (initialPath && conflicts.find((c) => c.path === initialPath)?.id) || conflicts[0]?.id
  )
  const [checked, setChecked] = useState<Set<string>>(new Set())
  const active = conflicts.find((c) => c.id === activeId) ?? conflicts[0]
  const connName = (id: string) => connections.find((c) => c.id === id)?.name ?? id

  const groups = useMemo(() => {
    const m = new Map<string, Conflict[]>()
    for (const c of conflicts) {
      const k = `${c.folderId}|${c.connectionId}`
      m.set(k, [...(m.get(k) ?? []), c])
    }
    return [...m.entries()]
  }, [conflicts])

  const doResolve = (ids: string[], how: Resolution) => {
    const idx = conflicts.findIndex((c) => c.id === active?.id)
    resolve(ids, how)
    setChecked(new Set())
    const rest = conflicts.filter((c) => !ids.includes(c.id))
    setActiveId(rest[Math.min(idx, rest.length - 1)]?.id)
    toast.success(t('conflicts.resolved', { count: ids.length }), { description: t('conflicts.resolvedDesc') })
  }

  return (
    <PageShell
      fixed
      fluid
      title={t('conflicts.title')}
      description={t('conflicts.description')}
      breadcrumb={`${t('nav.overview')} / ${t('nav.conflicts')}`}
    >
      {conflicts.length === 0 ? (
        <div className='flex flex-1 flex-col items-center justify-center gap-3 rounded-lg border border-dashed'>
          <CheckCircle2 className='size-10 text-emerald-500' />
          <div className='text-lg font-medium'>{t('conflicts.allClear')}</div>
          <p className='text-sm text-muted-foreground'>{t('conflicts.allClearDesc')}</p>
        </div>
      ) : (
        <div className='flex min-h-0 flex-1 gap-4'>
          {/* Queue */}
          <div className='flex w-80 shrink-0 flex-col overflow-hidden rounded-lg border bg-card 2xl:w-96'>
            <div className='flex h-11 items-center gap-2 border-b px-3'>
              <Checkbox
                checked={checked.size === conflicts.length ? true : checked.size ? 'indeterminate' : false}
                onCheckedChange={(v) => setChecked(v ? new Set(conflicts.map((c) => c.id)) : new Set())}
                aria-label={t('conflicts.selectAll')}
              />
              {checked.size ? (
                <div className='flex flex-1 items-center justify-end gap-1'>
                  <span className='me-auto text-xs text-muted-foreground'>{t('conflicts.selected', { count: checked.size })}</span>
                  <Button size='sm' variant='outline' className='h-7 text-xs' onClick={() => doResolve([...checked], 'windows')}>
                    {t('conflicts.useWindows')}
                  </Button>
                  <Button size='sm' variant='outline' className='h-7 text-xs' onClick={() => doResolve([...checked], 'wsl')}>
                    {t('conflicts.useWsl')}
                  </Button>
                </div>
              ) : (
                <span className='text-sm text-muted-foreground'>{t('conflicts.waiting', { count: conflicts.length })}</span>
              )}
            </div>
            <div className='min-h-0 flex-1 overflow-y-auto'>
              {groups.map(([key, items]) => {
                const [folderId, connId] = key.split('|')
                return (
                  <div key={key}>
                    <div className='sticky top-0 z-10 border-b bg-muted/80 px-3 py-1.5 text-xs font-medium text-muted-foreground backdrop-blur'>
                      {folders.find((f) => f.id === folderId)?.name} ↔ {connName(connId)}
                    </div>
                    {items.map((c) => (
                      <div
                        key={c.id}
                        onClick={() => setActiveId(c.id)}
                        className={cn(
                          'flex cursor-pointer gap-2.5 border-b border-border/60 px-3 py-2.5 hover:bg-muted/50',
                          active?.id === c.id && 'bg-accent hover:bg-accent'
                        )}
                      >
                        <Checkbox
                          className='mt-0.5'
                          checked={checked.has(c.id)}
                          onClick={(e) => e.stopPropagation()}
                          onCheckedChange={(v) =>
                            setChecked((s) => {
                              const n = new Set(s)
                              if (v) n.add(c.id)
                              else n.delete(c.id)
                              return n
                            })
                          }
                        />
                        <div className='min-w-0 flex-1'>
                          <div className='truncate text-sm font-medium'>{c.path.split('/').pop()}</div>
                          <div className='truncate font-mono text-[11px] text-muted-foreground'>{c.path}</div>
                          <div className='mt-1 flex items-center gap-2 text-[11px] text-muted-foreground'>
                            <Badge variant='outline' className='h-4 px-1 text-[10px] font-normal'>
                              {t(`conflicts.kinds.${c.kind}.label`)}
                            </Badge>
                            {ago(c.detectedAt)}
                          </div>
                        </div>
                      </div>
                    ))}
                  </div>
                )
              })}
            </div>
          </div>

          {/* Detail */}
          {active && <ConflictView key={active.id} c={active} wslName={connName(active.connectionId)} onResolve={(how) => doResolve([active.id], how)} />}
        </div>
      )}
    </PageShell>
  )
}

function ConflictView({
  c,
  wslName,
  onResolve,
}: {
  c: Conflict
  wslName: string
  onResolve: (how: Resolution) => void
}) {
  const { t } = useTranslation()
  const newer = new Date(c.win.mtime) > new Date(c.wsl.mtime) ? 'windows' : 'wsl'
  const ext = c.path.includes('.') ? c.path.slice(c.path.lastIndexOf('.')) : ''
  const base = c.path.slice(0, c.path.length - ext.length)
  // Diff direction: the other side is shown relative to baseSide.
  const [baseSide, setBaseSide] = useState<Side>('windows')
  const [inline, setInline] = useState(false)
  const [imageMode, setImageMode] = useState<ImageMode>('side')
  const cmpSide: Side = baseSide === 'windows' ? 'wsl' : 'windows'
  const name = (s: Side) => (s === 'windows' ? t('common.windows') : wslName)
  const ver = (s: Side) => (s === 'windows' ? c.win : c.wsl)
  // Each side's content, fetched on demand (the WSL copy through the client).
  const [hexOffset, setHexOffset] = useState(0)
  const load = (side: Side) => ({
    queryKey: ['content', c.folderId, c.connectionId, c.path, side, c.id, ver(side).hash, hexOffset],
    queryFn: () => fetchContent({ folderId: c.folderId, connectionId: c.connectionId, path: c.path, side, offset: hexOffset }),
    placeholderData: (prev: FileContent | undefined) => prev,
    enabled: !ver(side).deleted,
    retry: false,
  })
  const winQ = useQuery(load('windows'))
  const wslQ = useQuery(load('wsl'))
  const q = (s: Side) => (s === 'windows' ? winQ : wslQ)
  const data = (s: Side): FileContent | undefined => q(s).data
  const loading = winQ.isLoading || wslQ.isLoading
  const failed = [winQ, wslQ].find((x) => x.error)?.error
  // A deleted side compares as empty.
  const kind = data('windows')?.type ?? data('wsl')?.type ?? c.content
  const textOf = (s: Side) => {
    const d = data(s)
    return d?.type === 'text' ? d.text : ''
  }
  const imageOf = (s: Side) => {
    const d = data(s)
    return d?.type === 'image' ? d.src : ''
  }
  const bytesOf = (s: Side) => {
    const d = data(s)
    return d?.type === 'binary' ? d.bytes : new Uint8Array()
  }
  return (
    <div className='flex min-w-0 flex-1 flex-col gap-4'>
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='min-w-0'>
          <div className='font-mono text-sm break-all'>{c.path}</div>
          <div className='mt-1 flex items-center gap-2 text-sm text-muted-foreground'>
            <FileWarning className='size-4 text-amber-500' />
            {t(`conflicts.kinds.${c.kind}.text`)}
          </div>
        </div>
        <Button
          variant='outline'
          size='sm'
          onClick={() => onResolve('both')}
          title={t('conflicts.keepBothTitle', { name: `${base}.wsl-${wslName}${ext}` })}
        >
          <Copy /> {t('conflicts.keepBoth')}
        </Button>
      </div>

      <div className='grid gap-3 md:grid-cols-2'>
        <VersionCard
          icon={Laptop}
          side={t('common.windows')}
          v={c.win}
          newer={newer === 'windows'}
          onUse={() => onResolve('windows')}
          useLabel={c.win.deleted ? t('conflicts.deleteToo', { side: wslName }) : t('conflicts.useVersion', { side: t('common.windows') })}
        />
        <VersionCard
          icon={Terminal}
          side={wslName}
          v={c.wsl}
          newer={newer === 'wsl'}
          onUse={() => onResolve('wsl')}
          useLabel={c.wsl.deleted ? t('conflicts.deleteToo', { side: t('common.windows') }) : t('conflicts.useVersion', { side: wslName })}
        />
      </div>

      <div className='flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card'>
        <div className='flex shrink-0 flex-wrap items-center gap-2 border-b bg-muted/40 px-2 py-1.5 text-xs text-muted-foreground'>
          <Segmented>
            {(['windows', 'wsl'] as Side[]).map((b) => (
              <SegButton key={b} on={baseSide === b} onClick={() => setBaseSide(b)}>
                {t('viewer.relative', { a: name(b === 'windows' ? 'wsl' : 'windows'), b: name(b) })}
              </SegButton>
            ))}
          </Segmented>
          <Button variant='ghost' size='icon' className='size-7' onClick={() => setBaseSide(cmpSide)} aria-label={t('viewer.swap')} title={t('viewer.swap')}>
            <ArrowLeftRight />
          </Button>
          {kind === 'text' && (
            <Segmented>
              <SegButton on={!inline} onClick={() => setInline(false)}>
                {t('viewer.sideBySide')}
              </SegButton>
              <SegButton on={inline} onClick={() => setInline(true)}>
                {t('viewer.inline')}
              </SegButton>
            </Segmented>
          )}
          {kind === 'image' && (
            <Segmented>
              {imageModes.map((m) => (
                <SegButton key={m} on={imageMode === m} onClick={() => setImageMode(m)}>
                  {t(`viewer.image.${m}`)}
                </SegButton>
              ))}
            </Segmented>
          )}
          {kind === 'binary' && <HexNav offset={hexOffset} size={Math.max(c.win.size, c.wsl.size)} onChange={setHexOffset} />}
          {!(kind === 'text' && inline) && kind !== 'image' && (
            <span className='ms-auto pe-2'>
              {t('viewer.direction', { a: name(baseSide), b: name(cmpSide) })}
            </span>
          )}
        </div>
        <div className='min-h-0 flex-1'>
          {loading && (
            <div className='flex h-full items-center justify-center gap-2 text-sm text-muted-foreground'>
              <Loader2 className='size-5 animate-spin' /> {t('viewer.loading')}
            </div>
          )}
          {!loading && failed && (
            <div className='flex h-full items-center justify-center gap-2 text-sm text-muted-foreground'>
              <TriangleAlert className='size-5 text-amber-500' />
              {t(`viewer.errors.${failed instanceof ContentFetchError ? failed.code : 'unreadable'}`, { name: wslName })}
            </div>
          )}
          {!loading && !failed && kind === 'text' && (
            <TextDiff
              key={`${c.id}/${baseSide}`}
              id={`conflict/${c.id}/${baseSide}`}
              original={textOf(baseSide)}
              modified={textOf(cmpSide)}
              language={c.language}
              inline={inline}
            />
          )}
          {!loading && !failed && kind === 'image' && (
            <ImageCompare
              base={imageOf(baseSide)}
              other={imageOf(cmpSide)}
              baseLabel={name(baseSide)}
              otherLabel={name(cmpSide)}
              mode={imageMode}
            />
          )}
          {!loading && !failed && kind === 'binary' && (
            <HexCompare
              base={bytesOf(baseSide)}
              other={bytesOf(cmpSide)}
              baseSize={ver(baseSide).size}
              otherSize={ver(cmpSide).size}
              baseLabel={name(baseSide)}
              otherLabel={name(cmpSide)}
              offset={hexOffset}
            />
          )}
        </div>
      </div>
    </div>
  )
}

function VersionCard({
  icon: Icon,
  side,
  v,
  newer,
  onUse,
  useLabel,
}: {
  icon: React.ElementType
  side: string
  v: ConflictVersion
  newer: boolean
  onUse: () => void
  useLabel: string
}) {
  const { t } = useTranslation()
  return (
    <div className='flex items-center gap-4 rounded-lg border bg-card p-4'>
      <Icon className='size-5 shrink-0 text-muted-foreground' />
      <div className='min-w-0 flex-1 space-y-0.5'>
        <div className='flex items-center gap-2 text-sm font-medium'>
          {side}
          {newer && !v.deleted && (
            <Badge variant='secondary' className='h-5 px-1.5 text-[10px]'>
              {t('conflicts.newer')}
            </Badge>
          )}
        </div>
        {v.deleted ? (
          <div className='flex items-center gap-1.5 text-xs text-rose-600 dark:text-rose-400'>
            <Trash2 className='size-3.5' /> {t('conflicts.deletedAgo', { ago: ago(v.mtime) })}
          </div>
        ) : (
          <div className='text-xs text-muted-foreground tabular-nums'>
            {formatBytes(v.size)} · {timestamp(v.mtime)} · <span className='font-mono'>{v.hash}</span>
          </div>
        )}
      </div>
      <Button size='sm' onClick={onUse} variant={v.deleted ? 'outline' : 'default'}>
        {useLabel}
      </Button>
    </div>
  )
}
