// Actions on tree items, shared by the connection page and the file browser.
import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Ban, GitCompareArrows, ListMinus, ListPlus, Trash2, Undo2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import i18n, { tx } from '@/i18n'
import { cn } from '@/lib/utils'
import { ago, formatBytes, formatCount, timestamp } from '@/lib/format'
import type { AnnotatedNode } from '@/lib/status'
import type { Connection, ItemStatus, Side, SyncFolder } from '@/lib/types'
import { useData } from '@/api/data'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { StatusBadge } from '@/components/status'

export const isPrivate = (s: ItemStatus) => s === 'private-win' || s === 'private-wsl' || s === 'private-both'

export const can = {
  addToScope: (n: AnnotatedNode) => isPrivate(n.status) || n.status === 'partial',
  cancelSync: (n: AnnotatedNode) =>
    n.status === 'synced' || n.status === 'syncing' || n.status === 'partial' || n.status === 'conflict',
  exclude: (n: AnnotatedNode) => n.status !== 'excluded',
  cancelExclusion: (n: AnnotatedNode) => n.exclusion?.reason === 'manual' && n.exclusion.path === n.path,
}

/** Translated explanation of why an item is (not) synced. */
export function reasonText(node: AnnotatedNode): string {
  const t = i18n.t
  if (node.exclusion)
    return tx(`reasons.${node.exclusion.reason}.detail`, node.exclusion.params)
  if (node.kind === 'symlink') return t('connection.symlinkNever')
  if (node.rule)
    return t(node.rule.negate ? 'connection.ruleExcludes' : 'connection.ruleIncludes', {
      rule: node.rule.source,
      line: node.rule.index + 1,
    })
  if (node.kind === 'dir' && node.counts.inScope > 0)
    return t('connection.noRulePartial', { count: node.counts.inScope })
  return t('connection.noRule')
}

export function useItemActions(folder: SyncFolder, conn: Connection) {
  const { t } = useTranslation()
  const { addRules, exclude, cancelExclusion } = useData()
  const [deleting, setDeleting] = useState<{ nodes: AnnotatedNode[]; side?: Side }>()

  const one = (nodes: AnnotatedNode[]) => (nodes.length === 1 ? nodes[0].path : undefined)

  const actions = {
    addToScope(nodes: AnnotatedNode[]) {
      const list = nodes.filter(can.addToScope)
      if (!list.length) return
      const rules = list.map((n) => (n.kind === 'dir' ? `${n.path}/` : n.path))
      addRules(folder.id, rules)
      toast.success(one(list) ? t('actions.addedToScope', { rule: rules[0] }) : t('actions.addedToScopeMany', { count: list.length }), {
        description: list.some((n) => n.status === 'private-both')
          ? t('actions.addedBothSides')
          : t('actions.appliesToAll', { folder: folder.name }),
      })
    },
    cancelSync(nodes: AnnotatedNode[]) {
      const list = nodes.filter(can.cancelSync)
      if (!list.length) return
      addRules(
        folder.id,
        list.map((n) => `!${n.path}${n.kind === 'dir' ? '/' : ''}`)
      )
      toast.success(
        one(list) ? t('actions.syncCancelled', { path: list[0].path }) : t('actions.syncCancelledMany', { count: list.length }),
        { description: t('actions.filesStay') }
      )
    },
    exclude(nodes: AnnotatedNode[]) {
      const list = nodes.filter(can.exclude)
      if (!list.length) return
      exclude(folder.id, list.map((n) => n.path))
      toast.success(
        one(list) ? t('actions.excluded', { path: list[0].path }) : t('actions.excludedMany', { count: list.length }),
        { description: t('actions.excludedDesc') }
      )
    },
    cancelExclusion(nodes: AnnotatedNode[]) {
      const list = nodes.filter(can.cancelExclusion)
      if (!list.length) {
        toast(t('actions.autoExclusionLocked'))
        return
      }
      cancelExclusion(folder.id, list.map((n) => n.path))
      toast.success(
        one(list)
          ? t('actions.exclusionCancelled', { path: list[0].path })
          : t('actions.exclusionCancelledMany', { count: list.length })
      )
    },
    requestDelete(nodes: AnnotatedNode[], side?: Side) {
      if (nodes.length) setDeleting({ nodes, side })
    },
  }

  const dialog = deleting && (
    <DeleteDialog nodes={deleting.nodes} initialSide={deleting.side} conn={conn} folder={folder} onClose={() => setDeleting(undefined)} />
  )

  return { actions, dialog }
}

export type ItemActions = ReturnType<typeof useItemActions>['actions']

function DeleteDialog({
  nodes,
  initialSide,
  conn,
  folder,
  onClose,
}: {
  nodes: AnnotatedNode[]
  initialSide?: Side
  conn: Connection
  folder: SyncFolder
  onClose: () => void
}) {
  const { t } = useTranslation()
  const { deleteOnSide } = useData()
  const sides = [
    { id: 'windows' as const, label: t('common.windows'), has: nodes.some((n) => n.win) },
    { id: 'wsl' as const, label: conn.name, has: nodes.some((n) => n.wsl) },
  ]
  const [side, setSide] = useState<Side>(initialSide ?? (sides[1].has ? 'wsl' : 'windows'))
  const scoped = nodes.some((n) => !isPrivate(n.status) && n.status !== 'excluded')
  const sideName = side === 'windows' ? t('common.windows') : conn.name
  return (
    <ConfirmDialog
      open
      onOpenChange={(o) => !o && onClose()}
      destructive
      title={nodes.length === 1 ? t('deleteDialog.title') : t('deleteDialog.titleMany', { count: nodes.length })}
      desc={
        <div className='space-y-3'>
          <div className='max-h-24 overflow-y-auto font-mono text-xs break-all'>
            {nodes.slice(0, 20).map((n) => (
              <div key={n.path}>{n.path}</div>
            ))}
            {nodes.length > 20 && <div>…</div>}
          </div>
          {!initialSide && (
            <div className='grid grid-cols-2 gap-2'>
              {sides.map((s) => (
                <button
                  key={s.id}
                  type='button'
                  disabled={!s.has}
                  onClick={() => setSide(s.id)}
                  className={cn(
                    'rounded-md border px-3 py-2 text-start text-sm disabled:opacity-40',
                    side === s.id && s.has && 'border-destructive bg-destructive/5 text-foreground'
                  )}
                >
                  <div className='font-medium'>{s.label}</div>
                  <div className='text-xs'>{s.has ? t('deleteDialog.existsHere') : t('deleteDialog.notHere')}</div>
                </button>
              ))}
            </div>
          )}
          {scoped && (
            <p className='rounded-md bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400'>
              {t('deleteDialog.scopedWarn')}
            </p>
          )}
          <p className='text-xs'>{t('deleteDialog.trashNote')}</p>
        </div>
      }
      confirmText={t('deleteDialog.confirm', { side: sideName })}
      handleConfirm={() => {
        const paths = nodes.filter((n) => (side === 'windows' ? n.win : n.wsl)).map((n) => n.path)
        onClose()
        deleteOnSide(folder.id, conn.id, side, paths).then(
          () =>
            toast.success(t('deleteDialog.deleted', { side: sideName }), {
              description: paths.length === 1 ? paths[0] : undefined,
            }),
          () => {}
        )
      }}
    />
  )
}

/** Details panel: why an item is (not) synced, both sides' metadata, actions. */
export function ItemDetails({
  node,
  conn,
  folder,
  actions,
}: {
  node: AnnotatedNode
  conn: Connection
  folder: SyncFolder
  actions: ItemActions
}) {
  const { t } = useTranslation()
  const list = [node]
  return (
    <div className='space-y-5 p-4'>
      <div className='space-y-2'>
        <div className='text-sm font-semibold break-all'>{node.name}</div>
        <div className='font-mono text-xs break-all text-muted-foreground'>{node.path}</div>
        <StatusBadge status={node.status} />
      </div>

      <Section title={t('connection.why')}>
        <p className='text-sm'>{reasonText(node)}</p>
        {node.exclusion && (
          <p className='text-xs text-muted-foreground'>
            {t('connection.detectedOn', { origin: node.exclusion.origin, ago: ago(node.exclusion.since) })}
          </p>
        )}
        <Link
          to='/folders/$folderId'
          params={{ folderId: folder.id }}
          className='text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground'
        >
          {t('connection.editRules', { name: folder.name })}
        </Link>
      </Section>

      <Section title={t('connection.sides')}>
        <div className='grid grid-cols-[auto_1fr_1fr] gap-x-3 gap-y-1.5 text-xs'>
          <span />
          <span className='font-medium'>{t('common.windows')}</span>
          <span className='truncate font-medium'>{conn.name}</span>
          {node.kind === 'dir' ? (
            <>
              <span className='text-muted-foreground'>{t('connection.files')}</span>
              <span className='tabular-nums'>{node.win ? formatCount(node.counts.win) : '—'}</span>
              <span className='tabular-nums'>{node.wsl ? formatCount(node.counts.wsl) : '—'}</span>
              <span className='text-muted-foreground'>{t('connection.inScopeShort')}</span>
              <span className='col-span-2 tabular-nums'>{formatCount(node.counts.inScope)}</span>
            </>
          ) : (
            <>
              <span className='text-muted-foreground'>{t('connection.size')}</span>
              <span>{node.win ? formatBytes(node.win.size) : '—'}</span>
              <span>{node.wsl ? formatBytes(node.wsl.size) : '—'}</span>
              <span className='text-muted-foreground'>{t('connection.modified')}</span>
              <span>{node.win ? timestamp(node.win.mtime) : '—'}</span>
              <span>{node.wsl ? timestamp(node.wsl.mtime) : '—'}</span>
            </>
          )}
        </div>
      </Section>

      <Section title={t('connection.actions')}>
        <div className='flex flex-col gap-2'>
          {node.status === 'conflict' && node.kind !== 'dir' && (
            <Button size='sm' asChild>
              <Link to='/conflicts' search={{ path: node.path }}>
                <GitCompareArrows /> {t('actions.resolveConflict')}
              </Link>
            </Button>
          )}
          {can.addToScope(node) && (
            <Button size='sm' variant='outline' onClick={() => actions.addToScope(list)}>
              <ListPlus /> {t('actions.addToScope')}
            </Button>
          )}
          {can.cancelSync(node) && (
            <Button size='sm' variant='outline' onClick={() => actions.cancelSync(list)}>
              <ListMinus /> {t('actions.cancelSync')}
            </Button>
          )}
          {can.exclude(node) && (
            <Button size='sm' variant='outline' onClick={() => actions.exclude(list)}>
              <Ban /> {t('actions.exclude')}
            </Button>
          )}
          {can.cancelExclusion(node) && (
            <Button size='sm' variant='outline' onClick={() => actions.cancelExclusion(list)}>
              <Undo2 /> {t('actions.cancelExclusion')}
            </Button>
          )}
          <Button size='sm' variant='outline' className='text-destructive' onClick={() => actions.requestDelete(list)}>
            <Trash2 /> {t('actions.deleteOnSide')}
          </Button>
        </div>
      </Section>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className='space-y-2'>
      <h3 className='text-xs font-medium tracking-wide text-muted-foreground uppercase'>{title}</h3>
      {children}
    </section>
  )
}
