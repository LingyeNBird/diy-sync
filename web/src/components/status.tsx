import {
  AlertTriangle,
  Ban,
  CheckCircle2,
  CircleDashed,
  Laptop,
  Loader2,
  MonitorSmartphone,
  PauseCircle,
  Split,
  Terminal,
  WifiOff,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { Binding, ItemStatus, SyncFolder } from '@/lib/types'

export const statusMeta: Record<
  ItemStatus,
  { icon: React.ElementType; className: string; dot: string }
> = {
  synced: {
    icon: CheckCircle2,
    className: 'text-emerald-700 bg-emerald-500/10 ring-emerald-600/20 dark:text-emerald-400',
    dot: 'bg-emerald-500',
  },
  syncing: {
    icon: Loader2,
    className: 'text-sky-700 bg-sky-500/10 ring-sky-600/20 dark:text-sky-400',
    dot: 'bg-sky-500',
  },
  conflict: {
    icon: AlertTriangle,
    className: 'text-amber-700 bg-amber-500/10 ring-amber-600/25 dark:text-amber-400',
    dot: 'bg-amber-500',
  },
  excluded: {
    icon: Ban,
    className: 'text-rose-700 bg-rose-500/10 ring-rose-600/20 dark:text-rose-400',
    dot: 'bg-rose-500',
  },
  'private-win': {
    icon: Laptop,
    className: 'text-muted-foreground bg-muted ring-border',
    dot: 'bg-zinc-400',
  },
  'private-wsl': {
    icon: Terminal,
    className: 'text-violet-700 bg-violet-500/10 ring-violet-600/20 dark:text-violet-400',
    dot: 'bg-violet-500',
  },
  'private-both': {
    icon: Split,
    className: 'text-muted-foreground bg-muted ring-border',
    dot: 'bg-zinc-400',
  },
  partial: {
    icon: CircleDashed,
    className: 'text-teal-700 bg-teal-500/10 ring-teal-600/20 dark:text-teal-400',
    dot: 'bg-teal-500',
  },
}

export function StatusBadge({ status, className }: { status: ItemStatus; className?: string }) {
  const { t } = useTranslation()
  const m = statusMeta[status]
  const Icon = m.icon
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          className={cn(
            'inline-flex h-5 shrink-0 items-center gap-1 rounded-md px-1.5 text-[11px] font-medium whitespace-nowrap ring-1 ring-inset',
            m.className,
            className
          )}
        >
          <Icon className={cn('size-3', status === 'syncing' && 'animate-spin')} />
          {t(`status.${status}.label`)}
        </span>
      </TooltipTrigger>
      <TooltipContent>{t(`status.${status}.hint`)}</TooltipContent>
    </Tooltip>
  )
}

export function OnlineDot({ online, className }: { online: boolean; className?: string }) {
  return (
    <span className={cn('relative inline-flex size-2', className)}>
      {online && (
        <span className='absolute inline-flex size-full animate-ping rounded-full bg-emerald-400 opacity-60' />
      )}
      <span
        className={cn(
          'relative inline-flex size-2 rounded-full',
          online ? 'bg-emerald-500' : 'bg-zinc-400'
        )}
      />
    </span>
  )
}

const stateMeta = {
  idle: { icon: CheckCircle2, className: 'text-emerald-600 dark:text-emerald-400' },
  syncing: { icon: Loader2, className: 'text-sky-600 dark:text-sky-400' },
  conflict: { icon: AlertTriangle, className: 'text-amber-600 dark:text-amber-400' },
  paused: { icon: PauseCircle, className: 'text-muted-foreground' },
  offline: { icon: WifiOff, className: 'text-muted-foreground' },
} as const

export function SyncState({
  state,
  className,
}: {
  state: SyncFolder['state'] | Binding['state']
  className?: string
}) {
  const { t } = useTranslation()
  const m = stateMeta[state]
  const Icon = m.icon
  return (
    <span className={cn('inline-flex items-center gap-1.5 text-sm', m.className, className)}>
      <Icon className={cn('size-4', state === 'syncing' && 'animate-spin')} />
      {t(`syncState.${state}`)}
    </span>
  )
}

export function SideLabel({ side, name }: { side: 'windows' | 'wsl'; name?: string }) {
  const { t } = useTranslation()
  const Icon = side === 'windows' ? Laptop : MonitorSmartphone
  return (
    <span className='inline-flex items-center gap-1.5'>
      <Icon className='size-3.5 text-muted-foreground' />
      {side === 'windows' ? t('common.windows') : (name ?? t('common.wsl'))}
    </span>
  )
}
