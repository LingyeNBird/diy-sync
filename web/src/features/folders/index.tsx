import { Link } from '@tanstack/react-router'
import { FolderPlus, FolderSync } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { formatBytes, formatCount } from '@/lib/format'
import { useData } from '@/api/data'
import { AddFolderDialog } from './add-folder-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { PageShell } from '@/components/page-shell'
import { OnlineDot, SyncState } from '@/components/status'

export function Folders() {
  const { t } = useTranslation()
  const { folders, connections, conflicts, bindings } = useData()
  return (
    <PageShell
      title={t('folders.title')}
      description={t('folders.description')}
      breadcrumb={`${t('nav.sync')} / ${t('nav.folders')}`}
      actions={
        <AddFolderDialog
          trigger={
            <Button>
              <FolderPlus /> {t('folders.add')}
            </Button>
          }
        />
      }
    >
      <div className='overflow-hidden rounded-lg border bg-card'>
        <Table>
          <TableHeader>
            <TableRow className='bg-muted/40'>
              <TableHead className='ps-4'>{t('folders.colFolder')}</TableHead>
              <TableHead>{t('folders.colRules')}</TableHead>
              <TableHead>{t('folders.colClients')}</TableHead>
              <TableHead className='text-end'>{t('folders.colFiles')}</TableHead>
              <TableHead className='text-end'>{t('folders.colSize')}</TableHead>
              <TableHead className='pe-4'>{t('folders.colStatus')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {folders.map((f) => {
              const bs = bindings.filter((b) => b.folderId === f.id)
              const kc = conflicts.filter((c) => c.folderId === f.id).length
              const ruleCount = f.rules.filter((r) => r.trim() && !r.trim().startsWith('#')).length
              return (
                <TableRow key={f.id} className='h-16'>
                  <TableCell className='ps-4'>
                    <Link
                      to='/folders/$folderId'
                      params={{ folderId: f.id }}
                      className='flex items-center gap-3 hover:underline'
                    >
                      <FolderSync className='size-5 text-sky-500' />
                      <div>
                        <div className='font-medium'>{f.name}</div>
                        <div className='font-mono text-xs text-muted-foreground'>{f.winPath}</div>
                      </div>
                    </Link>
                  </TableCell>
                  <TableCell>
                    <div className='flex items-center gap-2'>
                      <span className='tabular-nums'>{ruleCount}</span>
                      {f.autoExcluded.length > 0 && (
                        <Badge variant='outline' className='text-rose-600 dark:text-rose-400'>
                          {t('folders.excludedCount', { count: f.autoExcluded.length })}
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className='flex flex-wrap gap-1.5'>
                      {bs.map((b) => {
                        const c = connections.find((x) => x.id === b.connectionId)!
                        return (
                          <Badge key={b.id} variant='secondary' className='gap-1.5 font-normal'>
                            <OnlineDot online={c.online} className='size-1.5 [&>span]:size-1.5' />
                            {c.name}
                          </Badge>
                        )
                      })}
                    </div>
                  </TableCell>
                  <TableCell className='text-end tabular-nums'>{formatCount(f.fileCount)}</TableCell>
                  <TableCell className='text-end tabular-nums'>{formatBytes(f.totalBytes)}</TableCell>
                  <TableCell className='pe-4'>
                    <SyncState state={kc ? 'conflict' : f.state} />
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>
    </PageShell>
  )
}
