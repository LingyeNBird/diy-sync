import { useEffect, useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import {
  AlertTriangle,
  ArrowDown,
  ArrowRight,
  ArrowUp,
  FolderSync,
  GitCompareArrows,
  MonitorSmartphone,
  ShieldCheck,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { ago, clock, formatBytes, formatCount, formatRate } from '@/lib/format'
import { rates as rateHistory, type RatePoint, useData } from '@/api/data'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { PageShell } from '@/components/page-shell'
import { OnlineDot, SyncState } from '@/components/status'
import { ActivityIcon, EventText } from '@/features/activity/activity-icon'

export function Dashboard() {
  const { t } = useTranslation()
  const { folders, connections, conflicts, pairing, activity, bindings } = useData()
  const rates = useLiveRates()
  const last = rates[rates.length - 1]
  const online = connections.filter((c) => c.online).length

  return (
    <PageShell
      title={t('dashboard.title')}
      description={t('dashboard.description')}
      breadcrumb={t('nav.overview')}
    >
      {(conflicts.length > 0 || pairing.length > 0) && (
        <div className='grid gap-3 md:grid-cols-2'>
          {conflicts.length > 0 && (
            <Attention
              icon={GitCompareArrows}
              tone='amber'
              title={t('dashboard.conflictsWaiting', { count: conflicts.length })}
              text={t('dashboard.conflictsWaitingText')}
              to='/conflicts'
              action={t('common.resolve')}
            />
          )}
          {pairing.map((p) => (
            <Attention
              key={p.code}
              icon={ShieldCheck}
              tone='sky'
              title={t('dashboard.wantsToConnect', { host: p.hostname })}
              text={t('dashboard.pairDetail', { distro: p.distro, address: p.address, code: p.code })}
              to='/pair'
              action={t('common.review')}
            />
          ))}
        </div>
      )}

      <div className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
        <Stat
          icon={FolderSync}
          label={t('dashboard.statFolders')}
          value={folders.length}
          sub={t('dashboard.filesSize', {
            files: formatCount(folders.reduce((n, f) => n + f.fileCount, 0)),
            size: formatBytes(folders.reduce((n, f) => n + f.totalBytes, 0)),
          })}
        />
        <Stat
          icon={MonitorSmartphone}
          label={t('dashboard.statConnections')}
          value={
            <>
              {online}
              <span className='text-base font-normal text-muted-foreground'>
                {t('dashboard.ofOnline', { total: connections.length })}
              </span>
            </>
          }
          sub={t('common.bindings', { count: bindings.length })}
        />
        <Stat
          icon={AlertTriangle}
          label={t('dashboard.statConflicts')}
          value={conflicts.length}
          sub={
            conflicts.length
              ? t('dashboard.oldest', { ago: ago(conflicts.map((c) => c.detectedAt).sort()[0]) })
              : t('dashboard.allClear')
          }
          tone={conflicts.length ? 'amber' : undefined}
        />
        <Stat
          icon={ArrowUp}
          label={t('dashboard.statThroughput')}
          value={formatRate(last.up + last.down)}
          sub={
            <span className='flex flex-col gap-0.5'>
              <span className='inline-flex items-center gap-0.5'>
                <ArrowUp className='size-3' /> {t('dashboard.toWsl', { rate: formatRate(last.up) })}
              </span>
              <span className='inline-flex items-center gap-0.5'>
                <ArrowDown className='size-3' /> {t('dashboard.toWindows', { rate: formatRate(last.down) })}
              </span>
            </span>
          }
        />
      </div>

      <div className='grid gap-4 lg:grid-cols-7'>
        <Card className='lg:col-span-4'>
          <CardHeader>
            <CardTitle>{t('dashboard.transferRate')}</CardTitle>
            <CardDescription>{t('dashboard.last5')}</CardDescription>
          </CardHeader>
          <CardContent className='h-64 ps-0'>
            <RateChart data={rates} />
          </CardContent>
        </Card>

        <Card className='lg:col-span-3'>
          <CardHeader className='flex flex-row items-center justify-between'>
            <div className='space-y-1.5'>
              <CardTitle>{t('nav.connections')}</CardTitle>
              <CardDescription>{t('dashboard.connectionsDesc')}</CardDescription>
            </div>
            <Button variant='ghost' size='sm' asChild>
              <Link to='/connections'>
                {t('common.all')} <ArrowRight />
              </Link>
            </Button>
          </CardHeader>
          <CardContent className='space-y-1'>
            {connections.map((c) => {
              const bs = bindings.filter((b) => b.connectionId === c.id)
              return (
                <Link
                  key={c.id}
                  to='/connections/$connectionId'
                  params={{ connectionId: c.id }}
                  className='-mx-2 flex items-center gap-3 rounded-md px-2 py-2 hover:bg-muted/60'
                >
                  <OnlineDot online={c.online} />
                  <div className='min-w-0 flex-1'>
                    <div className='truncate text-sm font-medium'>{c.name}</div>
                    <div className='truncate text-xs text-muted-foreground'>
                      {c.address} · {c.networkMode} · {t('common.bindings', { count: bs.length })}
                    </div>
                  </div>
                  <div className='text-end text-xs text-muted-foreground tabular-nums'>
                    {c.online ? (
                      <>
                        <div>↑ {formatRate(c.upBps)}</div>
                        <div>↓ {formatRate(c.downBps)}</div>
                      </>
                    ) : (
                      <span>{t('dashboard.seen', { ago: ago(c.lastSeen) })}</span>
                    )}
                  </div>
                </Link>
              )
            })}
          </CardContent>
        </Card>
      </div>

      <div className='grid gap-4 lg:grid-cols-7'>
        <Card className='lg:col-span-4'>
          <CardHeader className='flex flex-row items-center justify-between'>
            <div className='space-y-1.5'>
              <CardTitle>{t('nav.folders')}</CardTitle>
              <CardDescription>{t('dashboard.foldersDesc')}</CardDescription>
            </div>
            <Button variant='ghost' size='sm' asChild>
              <Link to='/folders'>
                {t('common.manage')} <ArrowRight />
              </Link>
            </Button>
          </CardHeader>
          <CardContent className='divide-y'>
            {folders.map((f) => {
              const fc = conflicts.filter((c) => c.folderId === f.id).length
              const bs = bindings.filter((b) => b.folderId === f.id)
              return (
                <Link
                  key={f.id}
                  to='/folders/$folderId'
                  params={{ folderId: f.id }}
                  className='-mx-2 flex items-center gap-4 rounded-md px-2 py-3 hover:bg-muted/60'
                >
                  <FolderSync className='size-5 text-sky-500' />
                  <div className='min-w-0 flex-1'>
                    <div className='flex items-center gap-2 text-sm font-medium'>
                      {f.name}
                      {fc > 0 && (
                        <Badge variant='outline' className='border-amber-500/40 text-amber-600 dark:text-amber-400'>
                          {t('common.conflicts', { count: fc })}
                        </Badge>
                      )}
                    </div>
                    <div className='truncate font-mono text-xs text-muted-foreground'>{f.winPath}</div>
                  </div>
                  <div className='hidden text-end text-xs text-muted-foreground sm:block'>
                    <div>{t('common.bindings', { count: bs.length })}</div>
                    <div>{t('common.files', { count: f.fileCount })}</div>
                  </div>
                  <SyncState state={fc ? 'conflict' : f.state} className='w-36 justify-end' />
                </Link>
              )
            })}
          </CardContent>
        </Card>

        <Card className='lg:col-span-3'>
          <CardHeader className='flex flex-row items-center justify-between'>
            <div className='space-y-1.5'>
              <CardTitle>{t('dashboard.recent')}</CardTitle>
              <CardDescription>{t('common.live')}</CardDescription>
            </div>
            <Button variant='ghost' size='sm' asChild>
              <Link to='/activity'>
                {t('common.log')} <ArrowRight />
              </Link>
            </Button>
          </CardHeader>
          <CardContent className='space-y-3'>
            {activity.slice(0, 7).map((e) => (
              <div key={e.id} className='flex gap-3 text-sm'>
                <ActivityIcon event={e} />
                <div className='min-w-0 flex-1'>
                  <div className='truncate'>
                    <EventText event={e} />
                  </div>
                  {e.path && <div className='truncate font-mono text-xs text-muted-foreground'>{e.path}</div>}
                </div>
                <span className='shrink-0 text-xs text-muted-foreground'>{ago(e.time)}</span>
              </div>
            ))}
          </CardContent>
        </Card>
      </div>
    </PageShell>
  )
}

function Stat({
  icon: Icon,
  label,
  value,
  sub,
  tone,
}: {
  icon: React.ElementType
  label: string
  value: React.ReactNode
  sub: React.ReactNode
  tone?: 'amber'
}) {
  return (
    <Card className='gap-2'>
      <CardHeader className='flex flex-row items-center justify-between space-y-0'>
        <CardTitle className='text-sm font-medium text-muted-foreground'>{label}</CardTitle>
        <Icon className={tone === 'amber' ? 'size-4 text-amber-500' : 'size-4 text-muted-foreground'} />
      </CardHeader>
      <CardContent>
        <div className='text-2xl font-semibold tabular-nums'>{value}</div>
        <div className='mt-1 text-xs text-muted-foreground'>{sub}</div>
      </CardContent>
    </Card>
  )
}

function Attention({
  icon: Icon,
  tone,
  title,
  text,
  to,
  action,
}: {
  icon: React.ElementType
  tone: 'amber' | 'sky'
  title: string
  text: string
  to: '/conflicts' | '/pair'
  action: string
}) {
  const cls =
    tone === 'amber'
      ? 'border-amber-500/30 bg-amber-500/5 [&_svg.lead]:text-amber-500'
      : 'border-sky-500/30 bg-sky-500/5 [&_svg.lead]:text-sky-500'
  return (
    <div className={`flex items-center gap-4 rounded-lg border p-4 ${cls}`}>
      <Icon className='lead size-5 shrink-0' />
      <div className='min-w-0 flex-1'>
        <div className='text-sm font-medium'>{title}</div>
        <div className='truncate text-xs text-muted-foreground'>{text}</div>
      </div>
      <Button size='sm' variant='outline' asChild>
        <Link to={to}>{action}</Link>
      </Button>
    </div>
  )
}

// Throughput sampled with every overview poll (see api/data.ts).
function useLiveRates() {
  const [data, setData] = useState<RatePoint[]>(() => rateHistory())
  useEffect(() => {
    const t = setInterval(() => setData(rateHistory()), 2000)
    return () => clearInterval(t)
  }, [])
  return data.length ? data : [{ t: new Date().toISOString(), up: 0, down: 0 }]
}

function RateChart({ data }: { data: RatePoint[] }) {
  const { t } = useTranslation()
  const rows = useMemo(
    () => data.map((p) => ({ t: clock(p.t), up: p.up / 1024 ** 2, down: p.down / 1024 ** 2 })),
    [data]
  )
  return (
    <ResponsiveContainer width='100%' height='100%' initialDimension={{ width: 640, height: 256 }}>
      <AreaChart data={rows} margin={{ left: 0, right: 12, top: 4 }}>
        <defs>
          <linearGradient id='gUp' x1='0' y1='0' x2='0' y2='1'>
            <stop offset='0%' stopColor='#0ea5e9' stopOpacity={0.35} />
            <stop offset='100%' stopColor='#0ea5e9' stopOpacity={0} />
          </linearGradient>
          <linearGradient id='gDown' x1='0' y1='0' x2='0' y2='1'>
            <stop offset='0%' stopColor='#8b5cf6' stopOpacity={0.3} />
            <stop offset='100%' stopColor='#8b5cf6' stopOpacity={0} />
          </linearGradient>
        </defs>
        <CartesianGrid vertical={false} strokeDasharray='3 3' className='stroke-border' />
        <XAxis dataKey='t' tickLine={false} axisLine={false} fontSize={11} minTickGap={48} stroke='currentColor' className='text-muted-foreground' />
        <YAxis tickLine={false} axisLine={false} fontSize={11} width={56} unit=' MB/s' stroke='currentColor' className='text-muted-foreground' />
        <Tooltip
          contentStyle={{
            background: 'var(--popover)',
            border: '1px solid var(--border)',
            borderRadius: 8,
            fontSize: 12,
          }}
          formatter={(v, name) => [`${Number(v).toFixed(1)} MB/s`, name === 'up' ? t('dashboard.chartUp') : t('dashboard.chartDown')]}
        />
        <Area type='monotone' dataKey='up' stroke='#0ea5e9' strokeWidth={2} fill='url(#gUp)' isAnimationActive={false} />
        <Area type='monotone' dataKey='down' stroke='#8b5cf6' strokeWidth={2} fill='url(#gDown)' isAnimationActive={false} />
      </AreaChart>
    </ResponsiveContainer>
  )
}
