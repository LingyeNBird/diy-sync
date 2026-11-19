import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Check, ShieldCheck, Terminal, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ago } from '@/lib/format'
import { useData } from '@/api/data'
import type { PairingRequest } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PageShell } from '@/components/page-shell'

/**
 * Verification page of the device flow (RFC 8628). `fdsync connect` prints
 * a link to this page and a code; the admin checks the code and approves.
 */
export function Pair({ code }: { code?: string }) {
  const { t } = useTranslation()
  const { pairing, approve, deny } = useData()
  const [entered, setEntered] = useState(code ?? '')
  const normalized = entered.toUpperCase().replace(/[^A-Z0-9]/g, '')
  const match = pairing.find((p) => p.code.replace('-', '') === normalized)

  return (
    <PageShell
      title={t('pair.title')}
      description={t('pair.description')}
      breadcrumb={`${t('nav.sync')} / ${t('nav.pairing')}`}
    >
      <div className='grid max-w-4xl gap-4 md:grid-cols-2'>
        <Card>
          <CardHeader>
            <CardTitle className='text-base'>{t('pair.enterCode')}</CardTitle>
            <CardDescription>{t('pair.enterCodeDesc')}</CardDescription>
          </CardHeader>
          <CardContent className='space-y-2'>
            <Label htmlFor='code'>{t('pair.code')}</Label>
            <Input
              id='code'
              value={entered}
              onChange={(e) => setEntered(e.target.value)}
              placeholder='XXXX-XXXX'
              className='h-12 text-center font-mono text-xl tracking-[0.3em] uppercase md:text-xl'
              maxLength={9}
            />
            {normalized.length === 8 && !match && (
              <p className='text-sm text-destructive'>{t('pair.noMatch')}</p>
            )}
          </CardContent>
        </Card>

        {match ? (
          <ApproveCard
            key={match.code}
            req={match}
            onApprove={(name) => {
              setEntered('')
              approve(match.code, name).then(
                () => toast.success(t('pair.paired', { name }), { description: t('pair.pairedDesc') }),
                () => {}
              )
            }}
            onDeny={() => {
              setEntered('')
              deny(match.code).then(() => toast(t('pair.denied')), () => {})
            }}
          />
        ) : (
          <Card className='border-dashed'>
            <CardHeader>
              <CardTitle className='text-base'>{t('pair.pending')}</CardTitle>
              <CardDescription>{t('pair.pendingDesc')}</CardDescription>
            </CardHeader>
            <CardContent className='space-y-2'>
              {pairing.length === 0 && <p className='text-sm text-muted-foreground'>{t('pair.noneNow')}</p>}
              {pairing.map((p) => (
                <button
                  key={p.code}
                  type='button'
                  onClick={() => setEntered(p.code)}
                  className='flex w-full items-center gap-3 rounded-md border px-3 py-2 text-start hover:bg-muted/50'
                >
                  <Terminal className='size-4 text-muted-foreground' />
                  <span className='flex-1 text-sm'>
                    {p.hostname} <span className='text-muted-foreground'>· {p.address}</span>
                  </span>
                  <span className='text-xs text-muted-foreground'>{ago(p.requestedAt)}</span>
                </button>
              ))}
              <p className='pt-2 text-xs text-muted-foreground'>
                {t('pair.fillHint')}
              </p>
            </CardContent>
          </Card>
        )}
      </div>
      <p className='text-sm text-muted-foreground'>
        {t('pair.footerBefore')}
        <Link to='/connections' className='underline underline-offset-2'>
          {t('nav.connections')}
        </Link>
        {t('pair.footerAfter')}
      </p>
    </PageShell>
  )
}

function ApproveCard({
  req,
  onApprove,
  onDeny,
}: {
  req: PairingRequest
  onApprove: (name: string) => void
  onDeny: () => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(req.hostname)
  return (
    <Card className='border-sky-500/40'>
      <CardHeader>
        <CardTitle className='flex items-center gap-2 text-base'>
          <ShieldCheck className='size-5 text-sky-500' /> {t('pair.request', { code: req.code })}
        </CardTitle>
        <CardDescription>{t('pair.requested', { ago: ago(req.requestedAt), expires: ago(req.expiresAt) })}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        <dl className='grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm'>
          <dt className='text-muted-foreground'>{t('pair.hostname')}</dt>
          <dd>{req.hostname}</dd>
          <dt className='text-muted-foreground'>{t('pair.distro')}</dt>
          <dd>{req.distro}</dd>
          <dt className='text-muted-foreground'>{t('pair.address')}</dt>
          <dd className='font-mono'>{req.address}</dd>
        </dl>
        <div className='space-y-2'>
          <Label htmlFor='name'>{t('pair.connName')}</Label>
          <Input id='name' value={name} onChange={(e) => setName(e.target.value)} />
        </div>
      </CardContent>
      <CardFooter className='gap-2'>
        <Button onClick={() => onApprove(name.trim() || req.hostname)}>
          <Check /> {t('pair.approve')}
        </Button>
        <Button variant='outline' onClick={onDeny}>
          <X /> {t('pair.deny')}
        </Button>
      </CardFooter>
    </Card>
  )
}
