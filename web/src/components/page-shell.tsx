import { ConfigDrawer } from '@/components/config-drawer'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { Search } from '@/components/search'
import { ThemeSwitch } from '@/components/theme-switch'
import { LanguageSwitch } from '@/components/language-switch'
import { PauseControl } from '@/components/pause-control'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

type PageShellProps = {
  title: React.ReactNode
  description?: React.ReactNode
  actions?: React.ReactNode
  breadcrumb?: React.ReactNode
  fixed?: boolean
  fluid?: boolean
  className?: string
  children: React.ReactNode
}

/** Standard page frame: top bar, title row and content. */
export function PageShell({
  title,
  description,
  actions,
  breadcrumb,
  fixed,
  fluid,
  className,
  children,
}: PageShellProps) {
  const { t } = useTranslation()
  return (
    <>
      <Header fixed>
        <div className='me-auto min-w-0 truncate text-sm text-muted-foreground'>
          {breadcrumb}
        </div>
        <PauseControl />
        <Search placeholder={t('header.jumpTo')} />
        <LanguageSwitch />
        <ThemeSwitch />
        <ConfigDrawer />
      </Header>
      <Main fixed={fixed} fluid={fluid} className={cn('flex flex-col gap-6', className)}>
        <div className='flex flex-wrap items-end justify-between gap-x-4 gap-y-3'>
          <div className='min-w-0 space-y-1'>
            <h1 className='text-2xl font-semibold tracking-tight'>{title}</h1>
            {description && (
              <p className='text-sm text-muted-foreground'>{description}</p>
            )}
          </div>
          {actions && <div className='flex flex-wrap items-center gap-2'>{actions}</div>}
        </div>
        {children}
      </Main>
    </>
  )
}
