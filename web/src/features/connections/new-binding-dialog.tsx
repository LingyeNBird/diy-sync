import { useState } from 'react'
import { ChevronRight, Folder, Home } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import type { Connection } from '@/lib/types'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'
import { useData } from '@/api/data'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

type ListResult = { path: string; home: string; dirs: string[]; files: number }

export function NewBindingDialog({ connection, trigger }: { connection: Connection; trigger: React.ReactNode }) {
  const { t } = useTranslation()
  const { folders, bindings, refresh } = useData()
  const [open, setOpen] = useState(false)
  const free = folders.filter((f) => !bindings.some((b) => b.connectionId === connection.id && b.folderId === f.id))
  const [folderId, setFolderId] = useState(free[0]?.id ?? '')
  const [path, setPath] = useState('') // '' = the client's home
  const [busy, setBusy] = useState(false)
  // The client lists its own folders (only under its allowed root).
  const ls = useQuery({
    queryKey: ['dirs', connection.id, path],
    queryFn: () => api<ListResult>('GET', `/api/connections/${encodeURIComponent(connection.id)}/dirs?path=${encodeURIComponent(path)}`),
    enabled: open && connection.online,
    retry: false,
  })
  const cur = ls.data
  const entries = cur?.dirs ?? []
  const rel = cur ? cur.path.slice(cur.home.length).split('/').filter(Boolean) : []
  const join = (...p: string[]) => [cur?.home ?? '', ...p].join('/').replace(/\/+/g, '/')

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>{t('newBinding.title', { name: connection.name })}</DialogTitle>
          <DialogDescription>{t('newBinding.desc')}</DialogDescription>
        </DialogHeader>

        <div className='space-y-2'>
          <Label>{t('newBinding.folder')}</Label>
          <Select value={folderId} onValueChange={setFolderId}>
            <SelectTrigger className='w-full'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {free.map((f) => (
                <SelectItem key={f.id} value={f.id}>
                  {f.name} <span className='font-mono text-xs text-muted-foreground'>{f.winPath}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className='space-y-2'>
          <Label>{t('newBinding.local', { name: connection.name })}</Label>
          <div className='overflow-hidden rounded-md border'>
            <div className='flex items-center gap-1 border-b bg-muted/40 px-2 py-1.5 font-mono text-xs'>
              <button type='button' onClick={() => setPath('')} className='flex items-center gap-1 rounded p-0.5 hover:bg-muted'>
                <Home className='size-3.5' /> <span className='text-muted-foreground'>{cur?.home}</span>
              </button>
              {rel.map((c, i) => (
                <span key={i} className='flex items-center gap-1'>
                  <ChevronRight className='size-3 text-muted-foreground' />
                  <button type='button' className='rounded px-0.5 hover:bg-muted' onClick={() => setPath(join(...rel.slice(0, i + 1)))}>
                    {c}
                  </button>
                </span>
              ))}
            </div>
            <div className='h-44 overflow-y-auto p-1'>
              {!connection.online && (
                <div className='p-4 text-center text-xs text-muted-foreground'>{t('newBinding.offline', { name: connection.name })}</div>
              )}
              {ls.isLoading && <div className='p-4 text-center text-xs text-muted-foreground'>{t('viewer.loading')}</div>}
              {ls.error && <div className='p-4 text-center text-xs text-destructive'>{ls.error.message}</div>}
              {cur && entries.length === 0 && (
                <div className='p-4 text-center text-xs text-muted-foreground'>
                  {cur.files === 0 ? t('newBinding.empty') : t('newBinding.noSubfolders', { count: cur.files })}
                </div>
              )}
              {entries.map((e) => (
                <button
                  key={e}
                  type='button'
                  onClick={() => setPath(join(...rel, e))}
                  className={cn('flex w-full items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-muted')}
                >
                  <Folder className='size-4 fill-sky-500/15 text-sky-500' /> {e}
                </button>
              ))}
            </div>
          </div>
          <p className='text-xs text-muted-foreground'>
            {t('newBinding.selected')} <span className='font-mono text-foreground'>{cur?.path ?? '…'}</span>.{' '}
            {t('newBinding.rootsNote')}
          </p>
        </div>

        <DialogFooter>
          <Button variant='outline' onClick={() => setOpen(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            disabled={!cur || !folderId || busy}
            onClick={() => {
              if (!cur) return
              setBusy(true)
              api('POST', `/api/connections/${encodeURIComponent(connection.id)}/bindings`, { folder: folderId, path: cur.path }).then(
                () => {
                  setBusy(false)
                  setOpen(false)
                  refresh()
                  toast.success(t('newBinding.created'), {
                    description: t('newBinding.createdDesc', { folder: folders.find((f) => f.id === folderId)?.name, path: cur.path }),
                  })
                },
                (e: Error) => {
                  setBusy(false)
                  toast.error(t('errors.actionFailed'), { description: e.message })
                }
              )
            }}
          >
            {t('newBinding.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
