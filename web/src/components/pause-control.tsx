// Pause all: stop every folder for a while (a big git rebase, a branch
// switch), so in-between states don't sync. Shown in every page's top bar.
import { Loader2, Pause, Play } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { clock } from '@/lib/format'
import { api } from '@/api/client'
import { useData } from '@/api/data'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const durations = [10, 30, 60, 120]

export function PauseControl() {
  const { t } = useTranslation()
  const { pause, refresh, loaded } = useData()
  const [busy, setBusy] = useState(false)
  if (!loaded) return null
  const set = (minutes: number) => {
    setBusy(true)
    api('PUT', '/api/pause', { minutes })
      .then(
        () => {
          refresh()
          toast.success(minutes === 0 ? t('pause.resumed') : t('pause.pausedToast'))
        },
        (e: Error) => toast.error(t('errors.actionFailed'), { description: e.message })
      )
      .finally(() => setBusy(false))
  }
  const label = pause.until ? t('pause.until', { time: clock(pause.until).slice(0, 5) }) : t('pause.indefinitely')
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant={pause.paused ? 'outline' : 'ghost'}
          size='sm'
          className={cn('h-8 gap-1.5', pause.paused && 'border-amber-500/50 bg-amber-500/10 text-amber-700 hover:bg-amber-500/20 dark:text-amber-300')}
          aria-label={pause.paused ? label : t('pause.pauseAll')}
        >
          {busy ? <Loader2 className='animate-spin' /> : <Pause />}
          <span className={cn(!pause.paused && 'sr-only lg:not-sr-only')}>{pause.paused ? label : t('pause.pauseAll')}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align='end' className='w-60'>
        <DropdownMenuLabel className='text-xs font-normal text-muted-foreground'>{t('pause.hint')}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {pause.paused && (
          <>
            <DropdownMenuItem onSelect={() => set(0)} className='font-medium'>
              <Play /> {t('pause.resume')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        {durations.map((m) => (
          <DropdownMenuItem key={m} onSelect={() => set(m)}>
            <Pause /> {m < 60 ? t('pause.forMinutes', { count: m }) : t('pause.forHours', { count: m / 60 })}
          </DropdownMenuItem>
        ))}
        <DropdownMenuItem onSelect={() => set(-1)}>
          <Pause /> {t('pause.untilResumed')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
