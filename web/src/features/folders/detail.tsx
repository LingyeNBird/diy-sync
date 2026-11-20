import { useDeferredValue, useEffect, useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { ChevronRight, FolderSync, RotateCcw, Save, Undo2 } from 'lucide-react'
import type { editor as MonacoEditor } from 'monaco-editor'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import i18n, { tx } from '@/i18n'
import { cn } from '@/lib/utils'
import { usePageState } from '@/lib/page-state'
import { ago, formatBytes, formatCount } from '@/lib/format'
import { previewMatches } from '@/lib/status'
import type { SyncFolder } from '@/lib/types'
import { api } from '@/api/client'
import { useTree, useData } from '@/api/data'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { CodeEditor } from '@/components/code-editor'
import { PageShell } from '@/components/page-shell'
import { OnlineDot, SyncState } from '@/components/status'

export function FolderDetail({ folderId }: { folderId: string }) {
  const { t } = useTranslation()
  const { folders, conflicts } = useData()
  const folder = folders.find((f) => f.id === folderId)
  const [tab, setTab] = usePageState(`folder/${folderId}/tab`, 'rules')
  if (!folder) return <div className='p-10'>{t('common.unknownFolder')}</div>
  const kc = conflicts.filter((c) => c.folderId === folder.id).length

  return (
    <PageShell
      breadcrumb={
        <span className='flex items-center gap-1'>
          <Link to='/folders' className='hover:text-foreground'>
            {t('nav.folders')}
          </Link>
          <ChevronRight className='size-3.5' />
          <span className='text-foreground'>{folder.name}</span>
        </span>
      }
      title={
        <span className='flex items-center gap-2.5'>
          <FolderSync className='size-6 text-sky-500' /> {folder.name}
        </span>
      }
      description={
        <span className='flex flex-wrap items-center gap-x-3 gap-y-1'>
          <span className='font-mono'>{folder.winPath}</span>
          <span>
            {t('common.files', { count: folder.fileCount })} · {formatBytes(folder.totalBytes)}
          </span>
          <SyncState state={kc ? 'conflict' : folder.state} className='text-xs' />
        </span>
      }
    >
      <Tabs value={tab} onValueChange={setTab} className='gap-4'>
        <TabsList>
          <TabsTrigger value='rules'>{t('folder.tabRules')}</TabsTrigger>
          <TabsTrigger value='excluded'>
            {t('folder.tabExcluded')}
            {folder.autoExcluded.length > 0 && (
              <Badge variant='secondary' className='ms-1 h-5 px-1.5'>
                {folder.autoExcluded.length}
              </Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value='bindings'>{t('folder.tabBindings')}</TabsTrigger>
          <TabsTrigger value='settings'>{t('folder.tabSettings')}</TabsTrigger>
        </TabsList>
        <TabsContent value='rules'>
          <RulesEditor folder={folder} />
        </TabsContent>
        <TabsContent value='excluded'>
          <Excluded folder={folder} />
        </TabsContent>
        <TabsContent value='bindings'>
          <Bindings folder={folder} />
        </TabsContent>
        <TabsContent value='settings'>
          <FolderSettings folder={folder} />
        </TabsContent>
      </Tabs>
    </PageShell>
  )
}

function RulesEditor({ folder }: { folder: SyncFolder }) {
  const { t } = useTranslation()
  const { setRules, bindings } = useData()
  const saved = folder.rules.join('\n')
  const [text, setText] = useState(saved)
  const deferred = useDeferredValue(text)
  const lines = useMemo(() => deferred.split('\n'), [deferred])
  const firstBinding = bindings.find((b) => b.folderId === folder.id)
  // Live preview runs on the first binding's tree (both sides' items).
  const { data: liveTree } = useTree(folder.id, firstBinding?.connectionId ?? '')
  const tree = useMemo(() => (firstBinding ? (liveTree ?? []) : []), [firstBinding, liveTree])
  const preview = useMemo(() => previewMatches(tree, lines, 400), [tree, lines])
  const dirty = text !== saved

  // Show per-rule match counts at the end of each line. Monaco draws them via
  // `afterContentClassName`; the text itself comes from a generated stylesheet.
  const [ed, setEd] = useState<MonacoEditor.IStandaloneCodeEditor>()
  const lang = i18n.language
  useEffect(() => {
    if (!ed) return
    const model = ed.getModel()
    if (!model) return
    const css: string[] = []
    const decos = [...preview.perRule.entries()]
      .filter(([line]) => line < model.getLineCount())
      .map(([line, n]) => {
        const label = i18n.t('folder.lineFiles', { count: n }).replace(/"/g, '\\"')
        css.push(`.fdsync-rc-${line}::after{content:"   ${label}"}`)
        const col = model.getLineMaxColumn(line + 1)
        return {
          range: { startLineNumber: line + 1, startColumn: col, endLineNumber: line + 1, endColumn: col },
          options: { afterContentClassName: `fdsync-rule-count fdsync-rc-${line}` },
        }
      })
    const style = document.createElement('style')
    style.textContent = css.join('\n')
    document.head.appendChild(style)
    const coll = ed.createDecorationsCollection(decos)
    return () => {
      coll.clear()
      style.remove()
    }
  }, [preview, ed, lang])

  return (
    <div className='grid gap-4 xl:grid-cols-5'>
      <Card className='gap-0 overflow-hidden py-0 xl:col-span-3'>
        <div className='flex items-center justify-between gap-3 border-b px-4 py-3'>
          <div>
            <div className='text-sm font-medium'>{t('folder.rules')}</div>
            <div className='text-xs text-muted-foreground'>
              {t('folder.sharedBy', { count: bindings.filter((b) => b.folderId === folder.id).length })}
            </div>
          </div>
          <div className='flex gap-2'>
            <Button size='sm' variant='ghost' disabled={!dirty} onClick={() => setText(saved)}>
              <RotateCcw /> {t('folder.discard')}
            </Button>
            <Button
              size='sm'
              disabled={!dirty}
              onClick={() => {
                setRules(folder.id, text.split('\n'))
                toast.success(t('folder.savedToast'), { description: t('folder.savedDesc') })
              }}
            >
              <Save /> {t('folder.saveRules')}
            </Button>
          </div>
        </div>
        <div className='h-[420px]'>
          <CodeEditor
            language='fdsync-rules'
            value={text}
            onChange={(v) => setText(v ?? '')}
            onMount={(e) => setEd(e)}
            options={{ lineNumbers: 'on', glyphMargin: false, folding: false, padding: { top: 12 } }}
          />
        </div>
        <div className='grid gap-x-6 gap-y-1 border-t bg-muted/30 px-4 py-3 font-mono text-xs text-muted-foreground sm:grid-cols-2'>
          <span>
            <b className='text-foreground'>a/</b> — {t('folder.helpDir')}
          </span>
          <span>
            <b className='text-foreground'>*.md</b> — {t('folder.helpExt')}
          </span>
          <span>
            <b className='text-foreground'>src/**/*.go</b> — {t('folder.helpDeep')}
          </span>
          <span>
            <b className='text-foreground'>!c/**/node_modules/</b> — {t('folder.helpNeg')}
          </span>
        </div>
      </Card>

      <Card className='gap-0 py-0 xl:col-span-2'>
        <CardHeader className='border-b py-3'>
          <CardTitle className='text-sm'>{t('folder.preview')}</CardTitle>
          <CardDescription>
            {t('folder.previewCount', { n: formatCount(preview.total), total: formatCount(preview.considered) })}
            {dirty && <span className='ms-2 text-amber-600 dark:text-amber-400'>{t('folder.unsaved')}</span>}
          </CardDescription>
        </CardHeader>
        <CardContent className='h-[468px] overflow-y-auto p-0'>
          {preview.matched.map((m) => (
            <div key={m.path} className='flex items-center gap-3 border-b border-border/50 px-4 py-1.5 text-xs'>
              <span className='min-w-0 flex-1 truncate font-mono'>{m.path}</span>
              <span className='shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground'>
                L{m.rule.index + 1}
              </span>
            </div>
          ))}
          {preview.total > preview.matched.length && (
            <div className='px-4 py-3 text-center text-xs text-muted-foreground'>
              {t('folder.more', { count: preview.total - preview.matched.length })}
            </div>
          )}
          {preview.total === 0 && (
            <div className='p-10 text-center text-sm text-muted-foreground'>{t('folder.noMatch')}</div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function Excluded({ folder }: { folder: SyncFolder }) {
  const { t } = useTranslation()
  const { cancelExclusion } = useData()
  if (folder.autoExcluded.length === 0)
    return (
      <Card>
        <CardContent className='py-10 text-center text-sm text-muted-foreground'>{t('folder.excludedEmpty')}</CardContent>
      </Card>
    )
  return (
    <div className='space-y-3'>
      <p className='text-sm text-muted-foreground'>{t('folder.excludedIntro')}</p>
      <div className='overflow-hidden rounded-lg border bg-card'>
        <Table>
          <TableHeader>
            <TableRow className='bg-muted/40'>
              <TableHead className='ps-4'>{t('folder.colPath')}</TableHead>
              <TableHead>{t('folder.colReason')}</TableHead>
              <TableHead>{t('folder.colDetail')}</TableHead>
              <TableHead>{t('folder.colFoundOn')}</TableHead>
              <TableHead>{t('folder.colSince')}</TableHead>
              <TableHead className='pe-4' />
            </TableRow>
          </TableHeader>
          <TableBody>
            {folder.autoExcluded.map((e) => (
              <TableRow key={e.path}>
                <TableCell className='ps-4 font-mono text-xs'>{e.path}</TableCell>
                <TableCell>
                  <Badge
                    variant='outline'
                    className={e.reason === 'manual' ? 'text-muted-foreground' : 'text-rose-600 dark:text-rose-400'}
                  >
                    {t(`reasons.${e.reason}.label`)}
                  </Badge>
                </TableCell>
                <TableCell className='max-w-80 truncate text-sm text-muted-foreground'>
                  {tx(`reasons.${e.reason}.detail`, e.params)}
                </TableCell>
                <TableCell className='text-sm'>{e.origin}</TableCell>
                <TableCell className='text-sm text-muted-foreground'>{ago(e.since)}</TableCell>
                <TableCell className='pe-4 text-end'>
                  {e.reason === 'manual' && (
                    <Button
                      size='sm'
                      variant='ghost'
                      className='h-7'
                      onClick={() => {
                        cancelExclusion(folder.id, [e.path])
                        toast.success(t('actions.exclusionCancelled', { path: e.path }))
                      }}
                    >
                      <Undo2 /> {t('actions.cancelExclusion')}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

function Bindings({ folder }: { folder: SyncFolder }) {
  const { t } = useTranslation()
  const { connections, bindings } = useData()
  const bs = bindings.filter((b) => b.folderId === folder.id)
  return (
    <div className='grid gap-3 md:grid-cols-2'>
      {bs.map((b) => {
        const c = connections.find((x) => x.id === b.connectionId)!
        return (
          <Link
            key={b.id}
            to='/connections/$connectionId'
            params={{ connectionId: c.id }}
            className='rounded-lg border bg-card p-4 transition-colors hover:bg-muted/40'
          >
            <div className='flex items-center gap-2 font-medium'>
              <OnlineDot online={c.online} /> {c.name}
            </div>
            <div className='mt-1 font-mono text-xs text-muted-foreground'>{b.localPath}</div>
            <div className='mt-3 flex items-center justify-between text-xs text-muted-foreground'>
              <span className='tabular-nums'>{t('folder.filesInScope', { count: b.inScopeCount || 0 })}</span>
              <SyncState state={c.online ? b.state : 'offline'} className='text-xs' />
            </div>
          </Link>
        )
      })}
    </div>
  )
}

function FolderSettings({ folder }: { folder: SyncFolder }) {
  const { t } = useTranslation()
  const { refresh } = useData()
  const save = (body: { conflictPolicy?: string; paused?: boolean; name?: string }) =>
    api('PUT', `/api/folders/${encodeURIComponent(folder.id)}/options`, body).then(
      () => {
        refresh()
        toast.success(t('common.saved'))
      },
      (e: Error) => toast.error(t('errors.actionFailed'), { description: e.message })
    )
  const options = [
    { v: 'ask', title: t('folder.policyAsk'), desc: t('folder.policyAskDesc') },
    { v: 'newest-wins', title: t('folder.policyNewest'), desc: t('folder.policyNewestDesc') },
  ]
  return (
    <div className='grid max-w-3xl gap-4'>
      <FolderName folder={folder} onSave={(name) => save({ name })} />
      <Card>
        <CardHeader>
          <CardTitle className='text-base'>{t('folder.policyTitle')}</CardTitle>
          <CardDescription>{t('folder.policyDesc')}</CardDescription>
        </CardHeader>
        <CardContent>
          <RadioGroup value={folder.conflictPolicy} onValueChange={(v) => void save({ conflictPolicy: v })} className='gap-3'>
            {options.map((o) => (
              <Label
                key={o.v}
                className={cn(
                  'flex cursor-pointer items-start gap-3 rounded-md border p-3 font-normal',
                  folder.conflictPolicy === o.v && 'border-primary/50 bg-muted/40'
                )}
              >
                <RadioGroupItem value={o.v} className='mt-0.5' />
                <span>
                  <span className='block font-medium'>{o.title}</span>
                  <span className='block text-sm text-muted-foreground'>{o.desc}</span>
                </span>
              </Label>
            ))}
          </RadioGroup>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className='text-base'>{t('folder.transfer')}</CardTitle>
        </CardHeader>
        <CardContent>
          <label className='flex items-center justify-between gap-6'>
            <span>
              <span className='block text-sm font-medium'>{t('folder.paused')}</span>
              <span className='block text-sm text-muted-foreground'>{t('folder.pausedDesc')}</span>
            </span>
            <Switch checked={folder.paused} onCheckedChange={(v) => void save({ paused: v })} />
          </label>
        </CardContent>
      </Card>
    </div>
  )
}

function FolderName({ folder, onSave }: { folder: SyncFolder; onSave: (name: string) => Promise<unknown> }) {
  const { t } = useTranslation()
  const [name, setName] = useState(folder.name === folder.id ? '' : folder.name)
  const saved = folder.name === folder.id ? '' : folder.name
  const dirty = name.trim() !== saved
  return (
    <Card>
      <CardHeader>
        <CardTitle className='text-base'>{t('folder.nameTitle')}</CardTitle>
        <CardDescription>{t('folder.nameDesc', { id: folder.id })}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className='flex flex-wrap gap-2'
          onSubmit={(e) => {
            e.preventDefault()
            if (dirty) void onSave(name.trim())
          }}
        >
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={folder.id} maxLength={64} className='h-9 max-w-sm' />
          <Button type='submit' size='sm' className='h-9' disabled={!dirty}>
            {t('common.save')}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
