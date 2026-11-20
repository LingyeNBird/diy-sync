import { useDeferredValue, useMemo, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import {
  Ban,
  ChevronRight,
  Columns2,
  FolderPlus,
  GitCompareArrows,
  ListMinus,
  ListPlus,
  MoreHorizontal,
  Pencil,
  Plus,
  Search,
  Trash2,
  Undo2,
  Unplug,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { usePageState } from '@/lib/page-state'
import { ago, formatCount } from '@/lib/format'
import { annotate, type AnnotatedNode } from '@/lib/status'
import type { Binding, Connection, ItemStatus, SyncFolder } from '@/lib/types'
import { api } from '@/api/client'
import { useTree, useData } from '@/api/data'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { FileTree } from '@/components/file-tree'
import { PageShell } from '@/components/page-shell'
import { OnlineDot, statusMeta } from '@/components/status'
import { can, ItemDetails, useItemActions, type ItemActions } from './item-actions'
import { NewBindingDialog } from './new-binding-dialog'

export function ConnectionDetail({ connectionId }: { connectionId: string }) {
  const { t } = useTranslation()
  const { connections, folders, bindings, revoke: revokeConn } = useData()
  const navigate = useNavigate()
  const conn = connections.find((c) => c.id === connectionId)
  const bs = bindings.filter((b) => b.connectionId === connectionId)
  const [active, setActive] = usePageState<string | undefined>(`conn/${connectionId}/tab`, () => bs[0]?.id)
  const [revoke, setRevoke] = useState(false)

  if (!conn) return <div className='p-10'>{t('common.unknownConnection')}</div>
  const binding = bs.find((b) => b.id === active)

  return (
    <PageShell
      fixed
      fluid
      breadcrumb={
        <span className='flex items-center gap-1'>
          <Link to='/connections' className='hover:text-foreground'>
            {t('nav.connections')}
          </Link>
          <ChevronRight className='size-3.5' />
          <span className='text-foreground'>{conn.name}</span>
        </span>
      }
      title={
        <span className='flex items-center gap-2.5'>
          {conn.name} <OnlineDot online={conn.online} />
        </span>
      }
      description={
        <>
          {conn.hostname && conn.hostname !== conn.name && (
            <>
              <span className='font-mono'>{conn.hostname}</span> ·{' '}
            </>
          )}
          {conn.distro} · <span className='font-mono'>{conn.address}</span> ·{' '}
          {t('connection.networking', { mode: conn.networkMode })} · {t('connection.client', { version: conn.version })} ·{' '}
          {conn.online ? t('connection.online') : t('connection.lastSeen', { ago: ago(conn.lastSeen) })}
        </>
      }
      actions={
        <>
          <Button variant='outline' size='sm' asChild>
            <Link to='/browse' search={{ connection: conn.id, binding: active }}>
              <Columns2 /> {t('connection.openBrowser')}
            </Link>
          </Button>
          <RenameDialog conn={conn} />
          <Button variant='outline' size='sm' className='text-destructive' onClick={() => setRevoke(true)}>
            <Unplug /> {t('common.revoke')}
          </Button>
        </>
      }
    >
      {conn.warnings?.includes('watch-limit') && (
        <div className='rounded-md border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-800 dark:text-amber-300'>
          <div className='font-medium'>{t('connection.watchLimit')}</div>
          <div className='mt-1'>{t('connection.watchLimitFix')}</div>
          <code className='mt-2 block rounded bg-background/60 px-2 py-1 font-mono text-xs'>
            echo fs.inotify.max_user_watches=1048576 | sudo tee /etc/sysctl.d/90-fdsync.conf && sudo sysctl --system
          </code>
        </div>
      )}
      <div className='flex flex-wrap items-center gap-3'>
        <Tabs value={active} onValueChange={setActive}>
          <TabsList>
            {bs.map((b) => {
              const f = folders.find((x) => x.id === b.folderId)!
              return (
                <TabsTrigger key={b.id} value={b.id} className='gap-2'>
                  <span className='font-medium'>{f.name}</span>
                  <span className='hidden font-mono text-xs text-muted-foreground md:inline'>{b.localPath}</span>
                </TabsTrigger>
              )
            })}
          </TabsList>
        </Tabs>
        <NewBindingDialog
          connection={conn}
          trigger={
            <Button variant='ghost' size='sm'>
              <Plus /> {t('connection.newBinding')}
            </Button>
          }
        />
      </div>

      {binding ? (
        <BindingView
          key={binding.id}
          binding={binding}
          folder={folders.find((f) => f.id === binding.folderId)!}
          conn={conn}
        />
      ) : (
        <div className='flex flex-1 flex-col items-center justify-center gap-3 rounded-lg border border-dashed text-muted-foreground'>
          <FolderPlus className='size-8' />
          <p>{t('connection.noBinding')}</p>
          <NewBindingDialog connection={conn} trigger={<Button>{t('connection.createBinding')}</Button>} />
        </div>
      )}

      <ConfirmDialog
        open={revoke}
        onOpenChange={setRevoke}
        destructive
        title={t('connection.revokeTitle', { name: conn.name })}
        desc={t('connection.revokeDesc')}
        confirmText={t('connection.revokeConfirm')}
        handleConfirm={() => {
          setRevoke(false)
          revokeConn(conn.id).then(() => {
            toast.success(t('connection.revoked', { name: conn.name }))
            void navigate({ to: '/connections' })
          }, () => {})
        }}
      />
    </PageShell>
  )
}

const filterable: ItemStatus[] = [
  'synced',
  'syncing',
  'conflict',
  'excluded',
  'private-win',
  'private-wsl',
  'private-both',
]

// Annotated trees, kept while their tree data is (React Query keeps an
// unchanged tree as the same object): coming back to a page doesn't redo
// the work. One entry per tree, for the rules and conflicts it was made with.
const annotated = new WeakMap<object, { key: string; value: ReturnType<typeof annotate> }>()
const emptyTree = annotate([], [], [], new Set())

export function useBindingTree(binding: Binding, folder: SyncFolder, conn: Connection) {
  const { conflicts } = useData()
  const { data: tree } = useTree(binding.folderId, binding.connectionId)
  // Keyed by the paths themselves, so an equal list never re-annotates the tree.
  const pathsKey = conflicts
    .filter((c) => c.connectionId === conn.id && c.folderId === folder.id)
    .map((c) => c.path)
    .join('\n')
  const key = JSON.stringify([folder.rules, folder.autoExcluded, pathsKey])
  const cached = tree ? annotated.get(tree) : undefined
  // A tree not prepared yet is prepared after the page has been drawn (with
  // a loading state), not while the old page is still on screen.
  const shown = useDeferredValue(tree, cached?.key === key ? tree : undefined)
  const value = useMemo(() => {
    if (!shown) return emptyTree
    const hit = annotated.get(shown)
    if (hit?.key === key) return hit.value
    const v = annotate(shown, folder.rules, folder.autoExcluded, new Set(pathsKey ? pathsKey.split('\n') : []))
    annotated.set(shown, { key, value: v })
    return v
    // key covers folder.rules, folder.autoExcluded and pathsKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shown, key])
  return { ...value, loading: !shown || shown !== tree }
}

/** Finds the current version of a node after the tree was rebuilt. */
export function findNode(nodes: AnnotatedNode[], path: string): AnnotatedNode | undefined {
  for (const n of nodes) {
    if (n.path === path) return n
    if (n.children && path.startsWith(n.path + '/')) return findNode(n.children, path)
  }
  return undefined
}

function BindingView({ binding, folder, conn }: { binding: Binding; folder: SyncFolder; conn: Connection }) {
  const { t } = useTranslation()
  const [query, setQuery] = usePageState(`conn/${binding.id}/query`, '')
  const [statuses, setStatuses] = usePageState<Set<ItemStatus>>(`conn/${binding.id}/statuses`, () => new Set())
  const [selected, setSelected] = usePageState<string | undefined>(`conn/${binding.id}/selected`, undefined)
  const { nodes, totals } = useBindingTree(binding, folder, conn)
  const { actions, dialog } = useItemActions(folder, conn)
  const current = selected ? findNode(nodes, selected) : undefined

  const toggleStatus = (s: ItemStatus) =>
    setStatuses((prev) => {
      const next = new Set(prev)
      if (next.has(s)) next.delete(s)
      else next.add(s)
      return next
    })

  const ruleCount = folder.rules.filter((r) => r.trim() && !r.trim().startsWith('#')).length

  return (
    <div className='flex min-h-0 flex-1 flex-col gap-4'>
      <div className='grid grid-cols-2 gap-px overflow-hidden rounded-lg border bg-border sm:grid-cols-3 xl:grid-cols-6'>
        <Metric label={t('connection.mWindows')} value={totals.win} sub={folder.winPath} mono />
        <Metric label={conn.name} value={totals.wsl} sub={binding.localPath} mono />
        <Metric label={t('connection.mInScope')} value={totals.inScope} sub={t('common.rules', { count: ruleCount })} />
        <Metric
          label={t('connection.mSyncing')}
          value={totals.syncing}
          sub={conn.online ? (totals.syncing ? t('connection.mPending') : t('syncState.idle')) : t('syncState.offline')}
        />
        <Metric
          label={t('connection.mConflicts')}
          value={totals.conflicts}
          tone={totals.conflicts ? 'amber' : undefined}
          sub={
            totals.conflicts ? (
              <Link to='/conflicts' className='underline underline-offset-2'>
                {t('common.resolve')}
              </Link>
            ) : (
              t('common.none')
            )
          }
        />
        <Metric
          label={t('connection.mExcluded')}
          value={totals.excluded}
          tone={totals.excluded ? 'rose' : undefined}
          sub={t('connection.mNeverSynced')}
        />
      </div>

      <div className='flex flex-wrap items-center gap-2'>
        <div className='relative w-full sm:w-72'>
          <Search className='absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground' />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('connection.filter')}
            className='h-8 ps-8'
          />
        </div>
        <div className='flex flex-wrap gap-1.5'>
          {filterable.map((s) => {
            const on = statuses.has(s)
            return (
              <button
                key={s}
                type='button'
                onClick={() => toggleStatus(s)}
                className={cn(
                  'inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs transition-colors',
                  on ? 'border-foreground/30 bg-accent text-foreground' : 'text-muted-foreground hover:bg-muted'
                )}
              >
                <span className={cn('size-2 rounded-full', statusMeta[s].dot)} />
                {t(`status.${s}.label`)}
              </button>
            )
          })}
          {statuses.size > 0 && (
            <Button variant='ghost' size='sm' className='h-7' onClick={() => setStatuses(new Set())}>
              {t('common.clear')}
            </Button>
          )}
        </div>
      </div>

      <div className='flex min-h-0 flex-1 gap-4'>
        <FileTree
          nodes={nodes}
          query={query}
          statuses={statuses}
          selected={current?.path}
          onSelect={(n) => setSelected(n.path)}
          wslLabel={conn.name}
          renderActions={(n) => <RowMenu node={n} actions={actions} />}
        />
        <aside className='hidden w-80 shrink-0 overflow-y-auto rounded-lg border bg-card xl:block'>
          {current ? (
            <ItemDetails node={current} conn={conn} folder={folder} actions={actions} />
          ) : (
            <div className='flex h-full items-center justify-center p-6 text-center text-sm text-muted-foreground'>
              {t('connection.selectHint')}
            </div>
          )}
        </aside>
      </div>

      {dialog}
    </div>
  )
}

function Metric({
  label,
  value,
  sub,
  mono,
  tone,
}: {
  label: string
  value: number
  sub: React.ReactNode
  mono?: boolean
  tone?: 'amber' | 'rose'
}) {
  return (
    <div className='min-w-0 bg-card px-4 py-3'>
      <div className='truncate text-xs text-muted-foreground'>{label}</div>
      <div
        className={cn(
          'text-xl font-semibold tabular-nums',
          tone === 'amber' && 'text-amber-600 dark:text-amber-400',
          tone === 'rose' && 'text-rose-600 dark:text-rose-400'
        )}
      >
        {formatCount(value)}
      </div>
      <div className={cn('truncate text-xs text-muted-foreground', mono && 'font-mono')}>{sub}</div>
    </div>
  )
}

function RowMenu({ node, actions }: { node: AnnotatedNode; actions: ItemActions }) {
  const { t } = useTranslation()
  const list = [node]
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button variant='ghost' size='icon' className='size-7 opacity-0 group-hover:opacity-100 data-[state=open]:opacity-100'>
          <MoreHorizontal />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align='end' className='w-56'>
        <DropdownMenuLabel className='truncate font-mono text-xs font-normal text-muted-foreground'>
          {node.path}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        {node.status === 'conflict' && node.kind !== 'dir' && (
          <DropdownMenuItem asChild>
            <Link to='/conflicts' search={{ path: node.path }}>
              <GitCompareArrows /> {t('actions.resolveConflict')}
            </Link>
          </DropdownMenuItem>
        )}
        {can.addToScope(node) && (
          <DropdownMenuItem onClick={() => actions.addToScope(list)}>
            <ListPlus /> {t('actions.addToScope')}
          </DropdownMenuItem>
        )}
        {can.cancelSync(node) && (
          <DropdownMenuItem onClick={() => actions.cancelSync(list)}>
            <ListMinus /> {t('actions.cancelSync')}
          </DropdownMenuItem>
        )}
        {can.exclude(node) && (
          <DropdownMenuItem onClick={() => actions.exclude(list)}>
            <Ban /> {t('actions.exclude')}
          </DropdownMenuItem>
        )}
        {can.cancelExclusion(node) && (
          <DropdownMenuItem onClick={() => actions.cancelExclusion(list)}>
            <Undo2 /> {t('actions.cancelExclusion')}
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem variant='destructive' onClick={() => actions.requestDelete(list)}>
          <Trash2 /> {t('actions.deleteOnSide')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// RenameDialog gives a connection an alias instead of its hostname.
function RenameDialog({ conn }: { conn: Connection }) {
  const { t } = useTranslation()
  const { refresh } = useData()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState(conn.name)
  const save = () => {
    const n = name.trim()
    if (!n) return
    api('PUT', `/api/connections/${encodeURIComponent(conn.id)}/name`, { name: n }).then(
      () => {
        setOpen(false)
        refresh()
        toast.success(t('connection.renamed', { name: n }))
      },
      (e: Error) => toast.error(t('errors.actionFailed'), { description: e.message })
    )
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) setName(conn.name)
      }}
    >
      <DialogTrigger asChild>
        <Button variant='outline' size='sm'>
          <Pencil /> {t('common.rename')}
        </Button>
      </DialogTrigger>
      <DialogContent className='sm:max-w-md'>
        <DialogHeader>
          <DialogTitle>{t('connection.renameTitle')}</DialogTitle>
          <DialogDescription>{t('connection.renameDesc', { host: conn.hostname || conn.id })}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            save()
          }}
        >
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={64} autoFocus />
          <DialogFooter className='mt-4'>
            <Button type='button' variant='outline' onClick={() => setOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button type='submit' disabled={!name.trim() || name.trim() === conn.name}>
              {t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
