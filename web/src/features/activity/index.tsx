import { useMemo, useState } from 'react'
import { Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { tx } from '@/i18n'
import { cn } from '@/lib/utils'
import { timestamp } from '@/lib/format'
import type { ActivityEvent } from '@/lib/types'
import { useData } from '@/api/data'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { PageShell } from '@/components/page-shell'
import { ActivityIcon, EventText } from './activity-icon'

export function Activity() {
  const { t } = useTranslation()
  const { activity, folders, connections } = useData()
  const [view, setView] = useState<'all' | 'audit' | 'problems'>('all')
  const [q, setQ] = useState('')

  const rows = useMemo(() => {
    const ql = q.toLowerCase()
    return activity.filter(
      (e: ActivityEvent) =>
        (view === 'all' || (view === 'audit' ? e.admin : e.level !== 'info')) &&
        (!ql || `${tx(`activity.events.${e.code}`, e.params)} ${e.path ?? ''}`.toLowerCase().includes(ql))
    )
  }, [activity, view, q])

  return (
    <PageShell
      title={t('activity.title')}
      description={t('activity.description')}
      breadcrumb={`${t('nav.overview')} / ${t('nav.activity')}`}
    >
      <div className='flex flex-wrap items-center gap-3'>
        <Tabs value={view} onValueChange={(v) => setView(v as typeof view)}>
          <TabsList>
            <TabsTrigger value='all'>{t('activity.all')}</TabsTrigger>
            <TabsTrigger value='problems'>{t('activity.problems')}</TabsTrigger>
            <TabsTrigger value='audit'>{t('activity.audit')}</TabsTrigger>
          </TabsList>
        </Tabs>
        <div className='relative w-full sm:w-72'>
          <Search className='absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground' />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('activity.search')} className='h-9 ps-8' />
        </div>
      </div>
      <div className='overflow-hidden rounded-lg border bg-card'>
        <Table>
          <TableHeader>
            <TableRow className='bg-muted/40'>
              <TableHead className='w-44 ps-4'>{t('activity.colTime')}</TableHead>
              <TableHead>{t('activity.colEvent')}</TableHead>
              <TableHead>{t('activity.colFolder')}</TableHead>
              <TableHead className='pe-4'>{t('activity.colConnection')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((e) => (
              <TableRow key={e.id}>
                <TableCell className='ps-4 font-mono text-xs text-muted-foreground'>{timestamp(e.time)}</TableCell>
                <TableCell>
                  <div className='flex gap-3'>
                    <ActivityIcon event={e} />
                    <div className='min-w-0'>
                      <div className={cn('text-sm', e.level === 'error' && 'text-rose-600 dark:text-rose-400')}>
                        <EventText event={e} />
                        {e.admin && (
                          <Badge variant='outline' className='ms-2 h-4 px-1 text-[10px]'>
                            {t('activity.admin')}
                          </Badge>
                        )}
                      </div>
                      {e.path && <div className='font-mono text-xs text-muted-foreground'>{e.path}</div>}
                    </div>
                  </div>
                </TableCell>
                <TableCell className='text-sm'>{folders.find((f) => f.id === e.folderId)?.name ?? '—'}</TableCell>
                <TableCell className='pe-4 text-sm'>
                  {connections.find((c) => c.id === e.connectionId)?.name ?? '—'}
                </TableCell>
              </TableRow>
            ))}
            {rows.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className='py-10 text-center text-muted-foreground'>
                  {t('activity.noMatch')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </PageShell>
  )
}
