// Shows one version of a file, or a diff of two (kept versions from the
// trash or the current file on a side).
import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowDown, ArrowLeftRight, ArrowUp, Laptop, Loader2, Terminal, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { timestamp } from '@/lib/format'
import type { FileContent } from '@/lib/content'
import type { Side, TrashItem } from '@/lib/types'
import { ContentFetchError, fetchContent } from '@/api/content'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { SegButton, Segmented } from '@/components/ui/segmented'
import { HexNav, HexView, ImageView, imageModes, TextView, type DiffNav, type DiffStats, type ImageMode } from '@/components/file-view'
import { Caption, Center, DiffBody, IconButton, Toggle } from '@/features/browser/viewer'

export type Version = {
  key: string
  side: Side
  connectionId: string
  label: string // the side's name
  trash?: string // kept version id; none = the current file
  when?: string
  size?: number
  current?: boolean
  item?: TrashItem
}

export function VersionViewer({
  path,
  folderId,
  versions,
  onClose,
}: {
  path: string
  folderId: string
  versions: Version[] // one to view, or two to compare (base first)
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [swapped, setSwapped] = useState(false)
  const [inline, setInline] = useState(false)
  const [wrap, setWrap] = useState(false)
  const [hideUnchanged, setHideUnchanged] = useState(false)
  const [imageMode, setImageMode] = useState<ImageMode>('side')
  const [fit, setFit] = useState(true)
  const [hexOffset, setHexOffset] = useState(0)
  const [stats, setStats] = useState<DiffStats>()
  const nav = useRef<DiffNav>(null)

  const isDiff = versions.length === 2
  const [base, cmp] = isDiff && swapped ? [versions[1], versions[0]] : [versions[0], versions[1]]

  const query = (v?: Version) => ({
    queryKey: ['content', 'trash', folderId, v?.key, path, hexOffset],
    queryFn: () => fetchContent({ folderId, connectionId: v!.connectionId, path, side: v!.side, trash: v!.trash, offset: hexOffset }),
    placeholderData: (prev: FileContent | undefined) => prev,
    enabled: !!v,
    retry: false,
    staleTime: 30_000,
  })
  const a = useQuery(query(base))
  const b = useQuery(query(cmp))
  const needed = isDiff ? [a, b] : [a]
  const loading = needed.some((r) => r.isLoading)
  const failed = needed.findIndex((r) => r.error)
  const ca = a.data
  const cb = b.data
  const kind = ca?.type ?? cb?.type
  const size = (c?: FileContent, v?: Version) => (c?.type === 'binary' ? (c.size ?? 0) : (v?.size ?? 0))

  const name = (v: Version) => (v.current ? t('trash.currentOn', { side: v.label }) : t('trash.keptOn', { side: v.label, when: timestamp(v.when!) }))
  const errText = (v: Version, e: unknown) => {
    const code = e instanceof ContentFetchError ? e.code : 'unreadable'
    if (code === 'missing') return v.current ? t('trash.noCurrent', { side: v.label }) : t('trash.gone')
    return t(`viewer.errors.${code}`, { name: v.label })
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className='flex h-[88vh] max-h-[88vh] w-[96vw] max-w-[96vw] flex-col gap-3 p-4 sm:max-w-[min(96vw,1600px)]'>
        <DialogHeader className='gap-1 pe-8'>
          <DialogTitle className='font-mono text-sm font-medium break-all'>{path}</DialogTitle>
          <DialogDescription className='flex flex-wrap gap-x-5 gap-y-1 text-xs'>
            {[base, cmp].filter(Boolean).map((v) => {
              const Icon = v!.side === 'windows' ? Laptop : Terminal
              return (
                <span key={v!.key} className='flex items-center gap-1.5'>
                  <Icon className='size-3.5' /> {name(v!)}
                </span>
              )
            })}
          </DialogDescription>
        </DialogHeader>

        <div className='flex flex-wrap items-center gap-2'>
          {isDiff && (
            <IconButton label={t('viewer.swap')} onClick={() => (setStats(undefined), setSwapped((s) => !s))}>
              <ArrowLeftRight />
            </IconButton>
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
          {kind === 'binary' && <HexNav offset={hexOffset} size={Math.max(size(ca, base), size(cb, cmp))} onChange={setHexOffset} />}
        </div>

        {isDiff && !loading && failed < 0 && kind === 'text' && !inline && (
          <div className='-mb-3 grid grid-cols-2 rounded-t-lg border border-b-0 bg-muted/40 text-xs text-muted-foreground'>
            <Caption role={t('viewer.base')} side={name(base)} />
            <Caption role={t('viewer.compared')} side={name(cmp!)} className='border-s' />
          </div>
        )}

        <div
          className={cn(
            'relative min-h-0 flex-1 overflow-hidden border bg-card',
            isDiff && !loading && failed < 0 && kind === 'text' && !inline ? 'rounded-b-lg' : 'rounded-lg'
          )}
        >
          {loading ? (
            <Center>
              <Loader2 className='size-5 animate-spin' /> {t('viewer.loading')}
            </Center>
          ) : failed >= 0 ? (
            <Center>
              <TriangleAlert className='size-5 text-amber-500' /> {errText(failed === 0 ? base : cmp!, needed[failed].error)}
            </Center>
          ) : isDiff ? (
            <DiffBody
              id={`trash/${folderId}/${base.key}/${cmp!.key}/${path}`}
              a={ca}
              b={cb}
              aSize={size(ca, base)}
              bSize={size(cb, cmp)}
              aLabel={name(base)}
              bLabel={name(cmp!)}
              inline={inline}
              wrap={wrap}
              hideUnchanged={hideUnchanged}
              imageMode={imageMode}
              hexOffset={hexOffset}
              onStats={setStats}
              navRef={nav}
            />
          ) : ca?.type === 'text' ? (
            <TextView text={ca.text} language={ca.language} wrap={wrap} modelPath={`trash/${folderId}/${base.key}/${path}`} />
          ) : ca?.type === 'image' ? (
            <ImageView src={ca.src} fit={fit} />
          ) : ca?.type === 'binary' ? (
            <HexView bytes={ca.bytes} size={ca.size ?? 0} offset={ca.offset ?? 0} />
          ) : null}
        </div>

        <div className='text-xs text-muted-foreground'>
          {isDiff && kind === 'text' && stats
            ? stats.changes === 0
              ? t('trash.identical')
              : t('viewer.stats', { added: stats.added, removed: stats.removed, changes: stats.changes })
            : isDiff
              ? t('trash.diffHint')
              : null}
        </div>
      </DialogContent>
    </Dialog>
  )
}
