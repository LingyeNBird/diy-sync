import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { ArrowRight, ArrowUpCircle, Columns2, Copy, FolderSync, Plus, ShieldCheck, Terminal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { ago, formatRate, isNewerVersion } from '@/lib/format'
import { useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { useData } from '@/api/data'
import { copyText } from '@/features/browser/rows'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { PageShell } from '@/components/page-shell'
import { OnlineDot, SyncState } from '@/components/status'

export function Connections() {
  const { t } = useTranslation()
  const { server, connections, folders, pairing, conflicts, bindings } = useData()
  return (
    <PageShell
      title={t('connections.title')}
      description={t('connections.description')}
      breadcrumb={`${t('nav.sync')} / ${t('nav.connections')}`}
      actions={<PairDialog />}
    >
      {pairing.map((p) => (
        <div key={p.code} className='flex items-center gap-4 rounded-lg border border-sky-500/30 bg-sky-500/5 p-4'>
          <ShieldCheck className='size-5 text-sky-500' />
          <div className='min-w-0 flex-1 text-sm'>
            {t('connections.waiting', { host: p.hostname, distro: p.distro, address: p.address })}{' '}
            <span className='font-mono font-medium'>{p.code}</span>
          </div>
          <Button size='sm' asChild>
            <Link to='/pair' search={{ code: p.code }}>
              {t('common.review')}
            </Link>
          </Button>
        </div>
      ))}

      {connections.length === 0 && (
        <div className='rounded-lg border border-dashed p-8 text-center text-muted-foreground'>
          <div className='font-medium text-foreground'>{t('browser.noConnections')}</div>
          <p className='mt-1 text-sm'>{t('connections.noneHint')}</p>
        </div>
      )}
      <div className='grid gap-4 lg:grid-cols-2 2xl:grid-cols-3'>
        {connections.map((c) => {
          const bs = bindings.filter((b) => b.connectionId === c.id)
          const kc = conflicts.filter((k) => k.connectionId === c.id).length
          const outdated = isNewerVersion(server?.version, c.version)
          return (
            <Card key={c.id} className='gap-4'>
              <CardHeader className='flex flex-row items-start gap-3'>
                <div className='flex size-10 items-center justify-center rounded-lg bg-muted'>
                  <Terminal className='size-5' />
                </div>
                <div className='min-w-0 flex-1 space-y-1'>
                  <CardTitle className='flex items-center gap-2'>
                    {c.name}
                    <OnlineDot online={c.online} />
                  </CardTitle>
                  <div className='text-xs text-muted-foreground'>
                    {c.distro} · <span className='font-mono'>{c.address}</span> · {c.networkMode}
                    {c.version ? ` · v${c.version}` : ''}
                  </div>
                </div>
                <div className='flex flex-wrap items-center justify-end gap-1.5'>
                  {outdated && server?.version && (
                    <button
                      type='button'
                      onClick={() => void copyText('fdsync upgrade').then(() => toast.success(t('connections.upgradeCopied')))}
                      title={t('connections.upgradeCopy')}
                      className='inline-flex items-center gap-1 rounded-full border border-sky-500/40 bg-sky-500/5 px-2 py-0.5 text-xs font-medium text-sky-600 transition-colors hover:bg-sky-500/15 dark:text-sky-400'
                    >
                      <ArrowUpCircle className='size-3.5' />
                      {t('connections.updateAvailable', { version: server.version })}
                    </button>
                  )}
                  {kc > 0 && (
                    <Badge variant='outline' className='border-amber-500/40 text-amber-600 dark:text-amber-400'>
                      {t('common.conflicts', { count: kc })}
                    </Badge>
                  )}
                </div>
              </CardHeader>
              <CardContent className='space-y-2'>
                {bs.length === 0 && (
                  <div className='rounded-md border border-dashed p-4 text-center text-sm text-muted-foreground'>
                    {t('connections.noBindings')}
                  </div>
                )}
                {bs.map((b) => {
                  const f = folders.find((x) => x.id === b.folderId)!
                  return (
                    <div key={b.id} className='flex items-center gap-3 rounded-md border px-3 py-2'>
                      <FolderSync className='size-4 text-sky-500' />
                      <div className='min-w-0 flex-1'>
                        <div className='text-sm font-medium'>{f.name}</div>
                        <div className='truncate font-mono text-xs text-muted-foreground'>{b.localPath}</div>
                      </div>
                      <SyncState state={c.online ? b.state : 'offline'} className='text-xs' />
                    </div>
                  )
                })}
              </CardContent>
              <CardFooter className='flex items-center justify-between text-xs text-muted-foreground'>
                <span className='tabular-nums'>
                  {c.online
                    ? `↑ ${formatRate(c.upBps)}  ↓ ${formatRate(c.downBps)}`
                    : t('connections.lastSeen', { ago: ago(c.lastSeen) })}
                  {' · '}
                  {t('connections.inScope', { count: bs.reduce((n, b) => n + b.inScopeCount, 0) })}
                </span>
                <div className='flex'>
                  <Button size='sm' variant='ghost' asChild>
                    <Link to='/browse' search={{ connection: c.id }}>
                      <Columns2 /> {t('connections.browse')}
                    </Link>
                  </Button>
                  <Button size='sm' variant='ghost' asChild>
                    <Link to='/connections/$connectionId' params={{ connectionId: c.id }}>
                      {t('common.open')} <ArrowRight />
                    </Link>
                  </Button>
                </div>
              </CardFooter>
            </Card>
          )
        })}
      </div>
    </PageShell>
  )
}

function PairDialog() {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  // Phase 1: clients connect with the server token. Pairing (fdsync connect) comes with TLS.
  const { data } = useQuery({
    queryKey: ['settings'],
    queryFn: () => api<{ listen: string; token: string; fingerprint: string }>('GET', '/api/settings'),
    enabled: open,
  })
  const port = data?.listen.split(':').pop() ?? '7443'
  const gw = "$(ip route | awk '/default/ {print $3}')"
  const cmd = !data
    ? '…'
    : data.fingerprint
      ? `fdsync connect https://${gw}:${port}`
      : `fdsync daemon --server http://${gw}:${port} \\\n  --token ${data.token}`
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus /> {t('connections.pairButton')}
        </Button>
      </DialogTrigger>
      <DialogContent className='sm:max-w-xl'>
        <DialogHeader>
          <DialogTitle>{t('connections.pairTitle')}</DialogTitle>
          <DialogDescription>{t('connections.connectDesc')}</DialogDescription>
        </DialogHeader>
        <div className='relative min-w-0'>
          <pre className='rounded-md bg-muted px-4 py-3 pe-12 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap'>{cmd}</pre>
          <Button
            variant='ghost'
            size='icon'
            className='absolute end-2 top-2 size-7'
            disabled={!data}
            aria-label={t('common.copied')}
            onClick={() => void copyText(cmd.replace(/\\\n\s*/g, '')).then(() => toast.success(t('common.copied')))}
          >
            <Copy />
          </Button>
        </div>
        <ul className='list-disc space-y-1 ps-5 text-sm text-muted-foreground'>
          {data?.fingerprint && <li>{t('connections.connectPair', { fp: data.fingerprint })}</li>}
          <li>{t('connections.connectGateway')}</li>
          <li>{t('connections.connectBind')}</li>
          <li>{t('connections.connectFirewall', { port })}</li>
        </ul>
      </DialogContent>
    </Dialog>
  )
}
