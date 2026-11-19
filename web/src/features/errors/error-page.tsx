import { useNavigate, useRouter } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'

type Kind = 'notFound' | 'general' | 'unauthorized' | 'forbidden' | 'maintenance'

export function ErrorPage({
  code,
  kind,
  minimal,
  className,
}: {
  code?: string
  kind: Kind
  minimal?: boolean
  className?: string
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { history } = useRouter()
  return (
    <div className={cn('h-svh w-full', className)}>
      <div className='m-auto flex h-full w-full flex-col items-center justify-center gap-2'>
        {!minimal && code && <h1 className='text-[7rem] leading-tight font-bold'>{code}</h1>}
        <span className='font-medium'>{t(`errors.${kind}`)}</span>
        <p className='max-w-md text-center text-muted-foreground'>{t(`errors.${kind}Desc`)}</p>
        {!minimal && kind !== 'maintenance' && (
          <div className='mt-6 flex gap-4'>
            <Button variant='outline' onClick={() => history.go(-1)}>
              {t('errors.goBack')}
            </Button>
            <Button onClick={() => navigate({ to: '/' })}>{t('errors.backHome')}</Button>
          </div>
        )}
      </div>
    </div>
  )
}
