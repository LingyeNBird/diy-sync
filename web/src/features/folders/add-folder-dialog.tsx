import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronRight, Folder, HardDrive } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { useData } from '@/api/data'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

type ListResult = { path: string; dirs: string[]; files: number }

const sepOf = (p: string) => (p.includes('\\') || /^[A-Za-z]:/.test(p) ? '\\' : '/')
const joinPath = (base: string, name: string) => {
  if (!base) return name // a drive like "C:\"
  const sep = sepOf(base)
  return base.endsWith(sep) ? base + name : base + sep + name
}
const parentOf = (p: string) => {
  const sep = sepOf(p)
  const trimmed = p.endsWith(sep) ? p.slice(0, -1) : p
  const i = trimmed.lastIndexOf(sep)
  if (i < 0) return ''
  const up = trimmed.slice(0, i + 1)
  return /^[A-Za-z]:\\$/.test(up) || up === '/' ? up : up.slice(0, -1)
}
const slug = (p: string) =>
  (p.split(/[\\/]/).filter(Boolean).pop() ?? '').replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^[-.]+/, '').slice(0, 64)

/** Adds a Windows folder to sync. The server lists its own folders. */
export function AddFolderDialog({ trigger }: { trigger: React.ReactNode }) {
  const { t } = useTranslation()
  const { folders, refresh } = useData()
  const [open, setOpen] = useState(false)
  const [path, setPath] = useState('') // '' = drives
  const [id, setId] = useState('')
  const [idTouched, setIdTouched] = useState(false)
  const [rules, setRules] = useState('')
  const [busy, setBusy] = useState(false)
  const ls = useQuery({
    queryKey: ['windirs', path],
    queryFn: () => api<ListResult>('GET', `/api/windirs?path=${encodeURIComponent(path)}`),
    enabled: open,
    retry: false,
  })
  const cur = ls.data
  const pick = (p: string) => {
    setPath(p)
    if (!idTouched) setId(slug(p))
  }
  const idTaken = folders.some((f) => f.id === id)
  const idValid = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(id)

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>{t('folders.addTitle')}</DialogTitle>
          <DialogDescription>{t('folders.addDesc')}</DialogDescription>
        </DialogHeader>

        <div className='space-y-2'>
          <Label>{t('folders.windowsFolder')}</Label>
          <div className='overflow-hidden rounded-md border'>
            <div className='flex items-center gap-1 border-b bg-muted/40 px-2 py-1.5 font-mono text-xs'>
              <button type='button' onClick={() => setPath('')} className='rounded p-0.5 hover:bg-muted' aria-label={t('folders.drives')}>
                <HardDrive className='size-3.5' />
              </button>
              {path && (
                <>
                  <ChevronRight className='size-3 text-muted-foreground' />
                  <button type='button' className='truncate rounded px-0.5 hover:bg-muted' onClick={() => pick(parentOf(path))}>
                    ..
                  </button>
                  <span className='truncate text-foreground'>{cur?.path ?? path}</span>
                </>
              )}
            </div>
            <div className='h-48 overflow-y-auto p-1'>
              {ls.isLoading && <div className='p-4 text-center text-xs text-muted-foreground'>{t('viewer.loading')}</div>}
              {ls.error && <div className='p-4 text-center text-xs text-destructive'>{ls.error.message}</div>}
              {cur && cur.dirs.length === 0 && (
                <div className='p-4 text-center text-xs text-muted-foreground'>{t('folders.noSubfolders')}</div>
              )}
              {cur?.dirs.map((d) => (
                <button
                  key={d}
                  type='button'
                  onClick={() => pick(joinPath(cur.path, d))}
                  className='flex w-full items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-muted'
                >
                  {cur.path ? <Folder className='size-4 fill-sky-500/15 text-sky-500' /> : <HardDrive className='size-4 text-muted-foreground' />} {d}
                </button>
              ))}
            </div>
          </div>
        </div>

        <div className='grid gap-4 sm:grid-cols-2'>
          <div className='space-y-2'>
            <Label htmlFor='folder-id'>{t('folders.id')}</Label>
            <Input
              id='folder-id'
              value={id}
              onChange={(e) => {
                setId(e.target.value)
                setIdTouched(true)
              }}
              className='font-mono'
            />
            <p className={idTaken || (id && !idValid) ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
              {idTaken ? t('folders.idTaken') : t('folders.idHint')}
            </p>
          </div>
          <div className='space-y-2'>
            <Label htmlFor='folder-rules'>{t('folders.firstRules')}</Label>
            <Textarea id='folder-rules' value={rules} onChange={(e) => setRules(e.target.value)} className='h-20 font-mono text-xs' placeholder={'src/\n!**/node_modules/'} />
          </div>
        </div>
        <p className='text-xs text-muted-foreground'>{t('folders.addNote')}</p>

        <DialogFooter>
          <Button variant='outline' onClick={() => setOpen(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            disabled={!path || !cur || !idValid || idTaken || busy}
            onClick={() => {
              setBusy(true)
              api('POST', '/api/folders', { id, path: cur?.path ?? path, rules: rules.split('\n').map((r) => r.trim()).filter(Boolean) }).then(
                () => {
                  setBusy(false)
                  setOpen(false)
                  refresh()
                  toast.success(t('folders.added', { id }))
                },
                (e: Error) => {
                  setBusy(false)
                  toast.error(t('errors.actionFailed'), { description: e.message })
                }
              )
            }}
          >
            {t('folders.add')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
