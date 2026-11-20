// Trash: every kept version on every side, searchable; compare any two
// (also with the current file) and restore one.
import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArchiveRestore, Eye, FileDiff, Laptop, Loader2, RefreshCw, Search, Terminal, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { remember, usePageState } from '@/lib/page-state'
import { ago, formatBytes, timestamp } from '@/lib/format'
import type { Side, TrashItem } from '@/lib/types'
import { api } from '@/api/client'
import { useData } from '@/api/data'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { PageShell } from '@/components/page-shell'
import { VersionViewer, type Version } from './compare'

type TrashList = {
  items: TrashItem[]
  unavailable: { connectionId: string; error: string }[]
}

type Group = { key: string; folderId: string; path: string; items: TrashItem[] }

const SHOWN = 300 // groups rendered at once; search narrows the rest

export function Trash({ initialQuery, initialFolder }: { initialQuery?: string; initialFolder?: string }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const { folders, connections, bindings } = useData()
  // A link (from the browser) sets the search; otherwise the page is as it was left.
  if (initialQuery !== undefined) remember('trash/q', initialQuery)
  if (initialFolder !== undefined) remember('trash/folder', initialFolder)
  const [q, setQ] = usePageState('trash/q', '')
  const [folder, setFolder] = usePageState('trash/folder', 'all')
  const [open, setOpen] = usePageState<string | undefined>('trash/open', undefined)
  const [selected, setSelected] = usePageState<string[]>('trash/selected', []) // version keys, at most 2
  const [viewing, setViewing] = useState<Version[]>()
  const [restoring, setRestoring] = useState<TrashItem>()
  const [busy, setBusy] = useState(false)

  const list = useQuery({
    queryKey: ['trash'],
    queryFn: () => api<TrashList>('GET', '/api/trash'),
    staleTime: 10_000,
  })
  const items = list.data?.items
  const connName = (id?: string) => connections.find((c) => c.id === id)?.name ?? id ?? ''
  const sideName = (side: Side, conn?: string) => (side === 'windows' ? t('common.windows') : connName(conn))
  const folderName = (id: string) => folders.find((f) => f.id === id)?.name ?? id

  const groups = useMemo(() => {
    const ql = q.trim().toLowerCase()
    const m = new Map<string, Group>()
    for (const it of items ?? []) {
      if (folder !== 'all' && it.folderId !== folder) continue
      if (ql && !it.path.toLowerCase().includes(ql)) continue
      const key = `${it.folderId}\0${it.path}`
      let g = m.get(key)
      if (!g) m.set(key, (g = { key, folderId: it.folderId, path: it.path, items: [] }))
      g.items.push(it)
    }
    const out = [...m.values()]
    for (const g of out) g.items.sort((a, b) => b.when.localeCompare(a.when))
    out.sort((a, b) => b.items[0].when.localeCompare(a.items[0].when))
    return out
  }, [items, q, folder])

  const group = groups.find((g) => g.key === open)
  // What each side's trash takes on disk, counted like its 50 MB limit (whole 4 KiB clusters).
  const sideBytes = useMemo(() => {
    const m = new Map<string, number>([['windows', 0]])
    for (const it of items ?? []) {
      const k = it.side === 'windows' ? 'windows' : it.connectionId!
      m.set(k, (m.get(k) ?? 0) + Math.max(4096, Math.ceil(it.size / 4096) * 4096))
    }
    return m
  }, [items])

  // The versions of the open file: kept ones, then the current file on each side.
  const versions: Version[] = useMemo(() => {
    if (!group) return []
    const kept: Version[] = group.items.map((it) => ({
      key: `${it.side}/${it.connectionId ?? ''}/${it.id}`,
      side: it.side,
      connectionId: it.connectionId ?? '',
      trash: it.id,
      when: it.when,
      size: it.size,
      label: sideName(it.side, it.connectionId),
      item: it,
    }))
    const bound = bindings.filter((b) => b.folderId === group.folderId)
    const anyConn = bound[0]?.connectionId ?? ''
    const current: Version[] = [
      { key: 'current/windows', side: 'windows', connectionId: anyConn, label: t('common.windows'), current: true },
      ...bound.map(
        (b): Version => ({ key: `current/wsl/${b.connectionId}`, side: 'wsl', connectionId: b.connectionId, label: connName(b.connectionId), current: true })
      ),
    ]
    return [...current, ...kept]
    // sideName/connName only read connections, which is listed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [group, bindings, connections, t])

  const toggle = (key: string, on: boolean) =>
    setSelected((s) => (on ? [...s.filter((k) => k !== key), key].slice(-2) : s.filter((k) => k !== key)))
  const openGroup = (key: string) => {
    setOpen(key)
    setSelected([])
  }
  const picked = versions.filter((v) => selected.includes(v.key))

  const restore = () => {
    const it = restoring
    if (!it) return
    setBusy(true)
    api('POST', '/api/trash/restore', { folder: it.folderId, side: it.side, connection: it.connectionId ?? '', id: it.id, path: it.path })
      .then(
        () => {
          toast.success(t('trash.restored', { path: it.path, where: sideName(it.side, it.connectionId) }))
          setRestoring(undefined)
          void qc.invalidateQueries({ queryKey: ['trash'] })
          void qc.invalidateQueries({ queryKey: ['content'] })
        },
        (e: Error) => toast.error(t('errors.actionFailed'), { description: e.message })
      )
      .finally(() => setBusy(false))
  }

  return (
    <PageShell
      title={t('trash.title')}
      description={t('trash.description')}
      breadcrumb={`${t('nav.overview')} / ${t('nav.trash')}`}
      actions={
        <Button variant='outline' size='sm' onClick={() => void list.refetch()} disabled={list.isFetching}>
          <RefreshCw className={cn(list.isFetching && 'animate-spin')} /> {t('trash.refresh')}
        </Button>
      }
    >
      <div className='flex flex-wrap items-center gap-3'>
        <div className='relative w-full sm:w-80'>
          <Search className='absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground' />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('trash.search')} className='h-9 ps-8' />
        </div>
        <Select value={folder} onValueChange={setFolder}>
          <SelectTrigger className='h-9 w-52' aria-label={t('trash.folder')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='all'>{t('trash.allFolders')}</SelectItem>
            {folders.map((f) => (
              <SelectItem key={f.id} value={f.id}>
                {f.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {list.data && (
          <div className='flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground tabular-nums'>
            {[...sideBytes].map(([id, n]) => (
              <span key={id} className='flex items-center gap-1'>
                {id === 'windows' ? <Laptop className='size-3.5' /> : <Terminal className='size-3.5' />}
                {id === 'windows' ? t('common.windows') : connName(id)}: {formatBytes(n)}
              </span>
            ))}
            <span>{t('trash.limit')}</span>
          </div>
        )}
      </div>

      {list.data?.unavailable.map((u) => (
        <div key={u.connectionId} className='flex items-center gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-sm'>
          <TriangleAlert className='size-4 shrink-0 text-amber-500' />
          {t('trash.unavailable', { name: connName(u.connectionId) })}
          <span className='truncate text-xs text-muted-foreground'>{u.error}</span>
        </div>
      ))}

      {list.isLoading ? (
        <div className='flex items-center justify-center gap-2 py-16 text-sm text-muted-foreground'>
          <Loader2 className='size-4 animate-spin' /> {t('trash.loading')}
        </div>
      ) : list.error ? (
        <div className='py-16 text-center text-sm text-muted-foreground'>{(list.error as Error).message}</div>
      ) : (
        <div className='grid min-h-0 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]'>
          {/* Files */}
          <div className='overflow-hidden rounded-lg border bg-card'>
            <div className='border-b bg-muted/40 px-4 py-2 text-xs text-muted-foreground'>
              {t('trash.files', { count: groups.length })}
            </div>
            <ul className='max-h-[70vh] divide-y overflow-y-auto'>
              {groups.slice(0, SHOWN).map((g) => {
                const sides = [...new Set(g.items.map((it) => (it.side === 'windows' ? 'windows' : it.connectionId!)))]
                return (
                  <li key={g.key}>
                    <button
                      type='button'
                      onClick={() => openGroup(g.key)}
                      className={cn('flex w-full flex-col gap-0.5 px-4 py-2 text-start hover:bg-muted/50', open === g.key && 'bg-muted')}
                    >
                      <span className='font-mono text-sm break-all'>{g.path}</span>
                      <span className='flex flex-wrap items-center gap-x-3 text-xs text-muted-foreground'>
                        <span>{folderName(g.folderId)}</span>
                        <span>{t('trash.versions', { count: g.items.length })}</span>
                        <span title={timestamp(g.items[0].when)}>{ago(g.items[0].when)}</span>
                        <span className='flex gap-1'>
                          {sides.map((s) => (
                            <Badge key={s} variant='outline' className='h-4 px-1 text-[10px] font-normal'>
                              {s === 'windows' ? t('common.windows') : connName(s)}
                            </Badge>
                          ))}
                        </span>
                      </span>
                    </button>
                  </li>
                )
              })}
              {groups.length > SHOWN && (
                <li className='px-4 py-3 text-center text-xs text-muted-foreground'>{t('trash.more', { count: groups.length - SHOWN })}</li>
              )}
              {groups.length === 0 && (
                <li className='px-4 py-12 text-center text-sm text-muted-foreground'>{items?.length ? t('trash.noMatch') : t('trash.empty')}</li>
              )}
            </ul>
          </div>

          {/* Versions of the open file */}
          <div className='overflow-hidden rounded-lg border bg-card'>
            {!group ? (
              <div className='flex h-full min-h-40 items-center justify-center p-6 text-center text-sm text-muted-foreground'>{t('trash.pick')}</div>
            ) : (
              <>
                <div className='flex flex-wrap items-center gap-2 border-b bg-muted/40 px-4 py-2'>
                  <span className='me-auto min-w-0 font-mono text-sm break-all'>{group.path}</span>
                  <Button size='sm' variant='outline' className='h-7' disabled={picked.length !== 1} onClick={() => setViewing(picked)}>
                    <Eye /> {t('trash.view')}
                  </Button>
                  <Button size='sm' className='h-7' disabled={picked.length !== 2} onClick={() => setViewing(sortOldFirst(picked))}>
                    <FileDiff /> {t('trash.compare')}
                  </Button>
                </div>
                <p className='px-4 pt-2 text-xs text-muted-foreground'>{t('trash.pickHint')}</p>
                <ul className='divide-y'>
                  {versions.map((v) => (
                    <li key={v.key} className={cn('flex items-center gap-3 px-4 py-2', selected.includes(v.key) && 'bg-primary/5')}>
                      <Checkbox
                        checked={selected.includes(v.key)}
                        onCheckedChange={(on) => toggle(v.key, on === true)}
                        aria-label={t('trash.select')}
                      />
                      {v.side === 'windows' ? <Laptop className='size-4 shrink-0' /> : <Terminal className='size-4 shrink-0' />}
                      <div className='min-w-0 flex-1'>
                        <div className='text-sm'>
                          <span className='font-medium'>{v.label}</span>
                          {' · '}
                          {v.current ? (
                            <span className='text-muted-foreground'>{t('trash.current')}</span>
                          ) : (
                            <span title={timestamp(v.when!)}>{t('trash.keptAt', { when: timestamp(v.when!) })}</span>
                          )}
                        </div>
                        {!v.current && (
                          <div className='text-xs text-muted-foreground tabular-nums'>
                            {formatBytes(v.size!)} · {ago(v.when!)}
                          </div>
                        )}
                      </div>
                      <Button variant='ghost' size='sm' className='h-7' onClick={() => setViewing([v])}>
                        <Eye /> <span className='sr-only sm:not-sr-only'>{t('trash.view')}</span>
                      </Button>
                      {v.item && (
                        <Button variant='outline' size='sm' className='h-7' onClick={() => setRestoring(v.item)}>
                          <ArchiveRestore /> <span className='sr-only sm:not-sr-only'>{t('trash.restore')}</span>
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              </>
            )}
          </div>
        </div>
      )}

      {viewing && group && <VersionViewer path={group.path} folderId={group.folderId} versions={viewing} onClose={() => setViewing(undefined)} />}

      <ConfirmDialog
        open={!!restoring}
        onOpenChange={(o) => !o && setRestoring(undefined)}
        title={t('trash.restoreTitle')}
        desc={
          restoring
            ? t('trash.restoreDesc', {
                path: restoring.path,
                when: timestamp(restoring.when),
                where: sideName(restoring.side, restoring.connectionId),
              })
            : ''
        }
        confirmText={t('trash.restore')}
        isLoading={busy}
        handleConfirm={restore}
      />
    </PageShell>
  )
}

// A comparison reads oldest → newest; the current file counts as newest.
function sortOldFirst(vs: Version[]) {
  const t = (v: Version) => (v.current ? '￿' : v.when!)
  return [...vs].sort((a, b) => t(a).localeCompare(t(b)))
}
