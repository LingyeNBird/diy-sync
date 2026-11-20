import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Download, ExternalLink, Eye, EyeOff, Loader2, RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { ago, formatBytes } from '@/lib/format'
import { languages } from '@/i18n'
import { api, getSession } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { PasswordInput } from '@/components/password-input'
import { PageShell } from '@/components/page-shell'
import { copyText } from '@/features/browser/rows'

type ServerSettings = {
  listen: string
  panelListen: string
  token: string
  trashMB: number
  trashUsed: number
  trashMax: number
  dataDir: string
  scanDelayMs: number
  rescanMin: number
  fingerprint: string
  allowToken: boolean
  /** Start at Windows login; absent when the server doesn't run on Windows. */
  autostart?: { enabled: boolean; error?: string }
}

const failed = (t: (k: 'errors.actionFailed') => string) => (e: Error) =>
  toast.error(t('errors.actionFailed'), { description: e.message })

export function Settings() {
  const { t, i18n } = useTranslation()
  const qc = useQueryClient()
  const { data: s } = useQuery({ queryKey: ['settings'], queryFn: () => api<ServerSettings>('GET', '/api/settings') })
  const [showToken, setShowToken] = useState(false)
  const [confirmRegen, setConfirmRegen] = useState(false)
  const [trashMB, setTrashMB] = useState<string>()
  const [pw, setPw] = useState({ current: '', next: '' })
  const reload = () => void qc.invalidateQueries({ queryKey: ['settings'] })

  return (
    <PageShell title={t('settings.title')} description={t('settings.description')} breadcrumb={`${t('nav.server')} / ${t('nav.settings')}`}>
      <div className='grid max-w-4xl gap-6'>
        <Card id='language'>
          <CardHeader>
            <CardTitle className='text-base'>{t('settings.language')}</CardTitle>
            <CardDescription>{t('settings.languageDesc')}</CardDescription>
          </CardHeader>
          <CardContent className='flex flex-wrap gap-2'>
            {languages.map((l) => (
              <button
                key={l.code}
                type='button'
                onClick={() => void i18n.changeLanguage(l.code)}
                className={cn(
                  'min-w-32 rounded-md border px-4 py-2 text-start text-sm transition-colors hover:bg-muted/50',
                  i18n.language === l.code && 'border-primary/50 bg-muted/60 font-medium'
                )}
              >
                {l.label}
                <span className='block font-mono text-xs text-muted-foreground'>{l.code}</span>
              </button>
            ))}
          </CardContent>
        </Card>

        <Updates />

        {s?.autostart && (
          <Card id='startup'>
            <CardHeader>
              <CardTitle className='text-base'>{t('settings.startup')}</CardTitle>
              <CardDescription>{t('settings.startupDesc')}</CardDescription>
            </CardHeader>
            <CardContent>
              <label className='flex items-center gap-3 text-sm'>
                <Switch
                  checked={s.autostart.enabled}
                  onCheckedChange={(v) =>
                    api('PUT', '/api/settings', { autostart: v }).then(() => {
                      reload()
                      toast.success(v ? t('settings.autostartOn') : t('settings.autostartOff'))
                    }, failed(t))
                  }
                />
                <span>
                  <span className='block font-medium'>{t('settings.autostart')}</span>
                  <span className='block text-xs text-muted-foreground'>{s.autostart.error ?? t('settings.autostartDesc')}</span>
                </span>
              </label>
            </CardContent>
          </Card>
        )}

        <Card id='network'>
          <CardHeader>
            <CardTitle className='text-base'>{t('settings.network')}</CardTitle>
            <CardDescription>{t('settings.networkDesc')}</CardDescription>
          </CardHeader>
          <CardContent className='grid gap-4 sm:grid-cols-2'>
            <ReadOnly label={t('settings.syncApi')} value={s?.listen} />
            <ReadOnly label={t('settings.panel')} value={s?.panelListen} note={t('settings.panelNote')} />
            <div className='sm:col-span-2'>
              <ReadOnly label={t('settings.dataDir')} value={s?.dataDir} />
            </div>
          </CardContent>
        </Card>

        <Card id='tls'>
          <CardHeader>
            <CardTitle className='text-base'>{t('settings.tls')}</CardTitle>
            <CardDescription>{s?.fingerprint ? t('settings.tlsDesc') : t('settings.tlsOff')}</CardDescription>
          </CardHeader>
          {s?.fingerprint && (
            <CardContent>
              <Label>{t('settings.fingerprint')}</Label>
              <div className='mt-1.5 flex items-center gap-2 rounded-md bg-muted px-3 py-2 font-mono text-xs'>
                <span className='flex-1 break-all'>{s.fingerprint}</span>
                <Button
                  variant='ghost'
                  size='icon'
                  className='size-7'
                  aria-label={t('common.copied')}
                  onClick={() => void copyText(s.fingerprint).then(() => toast.success(t('common.copied')))}
                >
                  <Copy />
                </Button>
              </div>
            </CardContent>
          )}
        </Card>

        <Card id='token'>
          <CardHeader>
            <CardTitle className='text-base'>{t('settings.token')}</CardTitle>
            <CardDescription>{t('settings.tokenDesc')}</CardDescription>
          </CardHeader>
          <CardContent>
            <div className='flex items-center gap-2 rounded-md bg-muted px-3 py-2 font-mono text-xs'>
              <span className='flex-1 break-all'>{s ? (showToken ? s.token : '•'.repeat(32)) : '…'}</span>
              <Button variant='ghost' size='icon' className='size-7' onClick={() => setShowToken((v) => !v)} aria-label={showToken ? t('settings.hide') : t('settings.show')}>
                {showToken ? <EyeOff /> : <Eye />}
              </Button>
              <Button
                variant='ghost'
                size='icon'
                className='size-7'
                disabled={!s}
                aria-label={t('common.copied')}
                onClick={() => s && void copyText(s.token).then(() => toast.success(t('common.copied')))}
              >
                <Copy />
              </Button>
            </div>
          </CardContent>
          <CardFooter className='flex flex-wrap items-center justify-between gap-4'>
            <label className='flex items-center gap-3 text-sm'>
              <Switch
                checked={s?.allowToken ?? true}
                disabled={!s}
                onCheckedChange={(v) =>
                  api('PUT', '/api/settings', { allowToken: v }).then(() => {
                    reload()
                    toast.success(v ? t('settings.tokenOn') : t('settings.tokenOff'))
                  }, failed(t))
                }
              />
              <span>
                <span className='block font-medium'>{t('settings.allowToken')}</span>
                <span className='block text-xs text-muted-foreground'>{t('settings.allowTokenDesc')}</span>
              </span>
            </label>
            <Button variant='outline' onClick={() => setConfirmRegen(true)}>
              <RefreshCw /> {t('settings.regenToken')}
            </Button>
          </CardFooter>
          <ConfirmDialog
            open={confirmRegen}
            onOpenChange={setConfirmRegen}
            destructive
            title={t('settings.regenToken')}
            desc={t('settings.regenTokenConfirm')}
            confirmText={t('settings.regenToken')}
            handleConfirm={() => {
              setConfirmRegen(false)
              api('POST', '/api/token').then(() => {
                reload()
                setShowToken(true)
                toast.success(t('settings.tokenRegenerated'))
              }, failed(t))
            }}
          />
        </Card>

        <Card id='trash'>
          <CardHeader>
            <CardTitle className='text-base'>{t('settings.trash')}</CardTitle>
            <CardDescription>{t('settings.trashDesc')}</CardDescription>
          </CardHeader>
          <CardContent className='space-y-2'>
            <Label htmlFor='trash-mb'>{t('settings.maxSize')}</Label>
            <div className='flex items-center gap-2'>
              <Input
                id='trash-mb'
                className='w-24 tabular-nums'
                inputMode='numeric'
                value={trashMB ?? String(s?.trashMB ?? '')}
                onChange={(e) => setTrashMB(e.target.value.replace(/\D/g, ''))}
              />
              <span className='text-sm text-muted-foreground'>MB</span>
              {s && <span className='ms-4 text-sm text-muted-foreground'>{t('settings.used', { size: formatBytes(s.trashUsed) })}</span>}
            </div>
            <p className='text-xs text-muted-foreground'>{t('settings.maxSizeNote', { max: s?.trashMax ?? 50 })}</p>
          </CardContent>
          <CardFooter>
            <Button
              disabled={trashMB === undefined || trashMB === ''}
              onClick={() =>
                api('PUT', '/api/settings', { trashMB: Math.min(Number(trashMB), s?.trashMax ?? 50) }).then(() => {
                  setTrashMB(undefined)
                  reload()
                  toast.success(t('common.saved'))
                }, failed(t))
              }
            >
              {t('common.save')}
            </Button>
          </CardFooter>
        </Card>

        <Card id='scan'>
          <CardHeader>
            <CardTitle className='text-base'>{t('settings.scanning')}</CardTitle>
            <CardDescription>{t('settings.scanningDesc')}</CardDescription>
          </CardHeader>
          <CardContent className='grid gap-4 sm:grid-cols-2'>
            <ReadOnly label={t('settings.batch')} value={s && `${s.scanDelayMs / 1000} s`} />
            <ReadOnly label={t('settings.rescan')} value={s && `${s.rescanMin} min`} />
          </CardContent>
        </Card>

        <Card id='password'>
          <CardHeader>
            <CardTitle className='text-base'>{t('settings.password')}</CardTitle>
          </CardHeader>
          <CardContent className='grid max-w-sm gap-3'>
            <div className='space-y-2'>
              <Label htmlFor='pw-cur'>{t('settings.current')}</Label>
              <PasswordInput id='pw-cur' value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} />
            </div>
            <div className='space-y-2'>
              <Label htmlFor='pw-new'>{t('settings.newPassword')}</Label>
              <PasswordInput id='pw-new' value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} />
            </div>
          </CardContent>
          <CardFooter>
            <Button
              disabled={!pw.current || pw.next.length < 8}
              onClick={() =>
                api('POST', '/api/password', { current: pw.current, password: pw.next }).then(() => {
                  setPw({ current: '', next: '' })
                  toast.success(t('settings.passwordChanged'))
                }, failed(t))
              }
            >
              {t('settings.changePassword')}
            </Button>
          </CardFooter>
        </Card>

        <Diagnostics />
      </div>
    </PageShell>
  )
}

type UpdateInfo = {
  current: string
  latest?: string
  newer: boolean
  url?: string
  checkedAt?: string
  error?: string
  canUpgrade: boolean
  installing: boolean
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/**
 * Waits for the restarted server to answer again, then reloads the panel.
 * The server scans every folder before it listens, so allow a few minutes.
 */
async function waitForRestart(): Promise<boolean> {
  await sleep(2000)
  for (let i = 0; i < 180; i++) {
    try {
      await getSession()
      return true
    } catch {
      await sleep(1000)
    }
  }
  return false
}

function Updates() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  // The server asks GitHub at most once an hour; opening this page checks.
  const { data: u, isFetching } = useQuery({
    queryKey: ['update'],
    queryFn: () => api<UpdateInfo>('GET', '/api/update'),
    staleTime: 5 * 60_000,
    refetchInterval: 60 * 60_000,
  })
  const [checking, setChecking] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const [phase, setPhase] = useState<'idle' | 'installing' | 'restarting'>('idle')

  const checkNow = () => {
    setChecking(true)
    api<UpdateInfo>('GET', '/api/update?refresh=1')
      .then((r) => qc.setQueryData(['update'], r), failed(t))
      .finally(() => setChecking(false))
  }

  const upgrade = () => {
    setConfirm(false)
    setPhase('installing')
    api<{ version: string }>('POST', '/api/update').then(
      async () => {
        setPhase('restarting')
        if (await waitForRestart()) window.location.reload()
        else {
          setPhase('idle')
          toast.warning(t('settings.upgradeSlow'))
        }
      },
      (e: Error) => {
        setPhase('idle')
        failed(t)(e)
      }
    )
  }

  const busy = phase !== 'idle' || !!u?.installing
  return (
    <Card id='updates'>
      <CardHeader>
        <CardTitle className='text-base'>{t('settings.updates')}</CardTitle>
        <CardDescription>{t('settings.updatesDesc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <div className='grid gap-4 sm:grid-cols-2'>
          <ReadOnly label={t('settings.currentVersion')} value={u?.current} />
          <ReadOnly label={t('settings.latestVersion')} value={u ? (u.latest ?? '—') : undefined} />
        </div>
        <p className='text-sm'>
          {!u || ((isFetching || checking) && !u.checkedAt) ? (
            <span className='text-muted-foreground'>{t('settings.checking')}</span>
          ) : u.error ? (
            <span className='text-destructive'>{u.error}</span>
          ) : u.newer ? (
            <span className='font-medium'>
              {t('settings.newVersion', { version: u.latest })} {!u.canUpgrade && <span className='text-muted-foreground'>{t('settings.noAsset')}</span>}
            </span>
          ) : (
            <span className='text-muted-foreground'>{t('settings.upToDate')}</span>
          )}
          {u?.url && (
            <a href={u.url} target='_blank' rel='noreferrer' className='ms-3 inline-flex items-center gap-1 text-sm underline-offset-4 hover:underline'>
              {t('settings.releaseNotes')} <ExternalLink className='size-3.5' />
            </a>
          )}
        </p>
        {u?.checkedAt && <p className='text-xs text-muted-foreground'>{t('settings.checkedAgo', { when: ago(u.checkedAt) })}</p>}
      </CardContent>
      <CardFooter className='flex flex-wrap gap-2'>
        <Button variant='outline' disabled={checking || busy} onClick={checkNow}>
          {checking ? <Loader2 className='animate-spin' /> : <RefreshCw />} {t('settings.checkNow')}
        </Button>
        {u?.canUpgrade && (
          <Button disabled={busy} onClick={() => setConfirm(true)}>
            {busy ? <Loader2 className='animate-spin' /> : <Download />}
            {phase === 'restarting'
              ? t('settings.restarting', { version: u.latest })
              : busy
                ? t('settings.upgrading')
                : t('settings.upgradeTo', { version: u.latest })}
          </Button>
        )}
      </CardFooter>
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={t('settings.upgradeTo', { version: u?.latest })}
        desc={t('settings.upgradeConfirm', { version: u?.latest })}
        confirmText={t('settings.upgradeTo', { version: u?.latest })}
        handleConfirm={upgrade}
      />
    </Card>
  )
}

function Diagnostics() {
  const { t } = useTranslation()
  return (
    <Card id='diagnostics'>
      <CardHeader>
        <CardTitle className='text-base'>{t('settings.diagnostics')}</CardTitle>
        <CardDescription>{t('settings.diagnosticsDesc')}</CardDescription>
      </CardHeader>
      <CardFooter>
        <Button
          variant='outline'
          onClick={() =>
            api('POST', '/api/debug/stacks').then(
              () => toast.success(t('settings.dumpWritten')),
              (e: Error) => toast.error(t('errors.actionFailed'), { description: e.message })
            )
          }
        >
          {t('settings.writeDump')}
        </Button>
      </CardFooter>
    </Card>
  )
}

function ReadOnly({ label, value, note }: { label: string; value?: string; note?: string }) {
  return (
    <div className='space-y-1.5'>
      <Label>{label}</Label>
      <div className='rounded-md border bg-muted/40 px-3 py-2 font-mono text-sm break-all'>{value ?? '…'}</div>
      {note && <p className='text-xs text-muted-foreground'>{note}</p>}
    </div>
  )
}
