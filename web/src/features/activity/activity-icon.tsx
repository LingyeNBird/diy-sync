import {
  ArchiveRestore,
  ArrowLeftRight,
  Ban,
  CheckCheck,
  FilePen,
  GitCompareArrows,
  Link2,
  ListFilter,
  Plug,
  Trash2,
  Unplug,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { tx } from '@/i18n'
import { cn } from '@/lib/utils'
import type { ActivityEvent } from '@/lib/types'

const icons: Record<ActivityEvent['kind'], React.ElementType> = {
  sync: ArrowLeftRight,
  delete: Trash2,
  rename: FilePen,
  conflict: GitCompareArrows,
  resolve: CheckCheck,
  scope: ListFilter,
  exclude: Ban,
  connect: Plug,
  disconnect: Unplug,
  pair: Link2,
  restore: ArchiveRestore,
}

export function ActivityIcon({ event }: { event: ActivityEvent }) {
  const Icon = icons[event.kind]
  return (
    <span
      className={cn(
        'mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md',
        event.level === 'error'
          ? 'bg-rose-500/10 text-rose-600 dark:text-rose-400'
          : event.level === 'warn'
            ? 'bg-amber-500/10 text-amber-600 dark:text-amber-400'
            : 'bg-muted text-muted-foreground'
      )}
    >
      <Icon className='size-3.5' />
    </span>
  )
}

/** Translated message of an activity event (from its code and params). */
export function EventText({ event }: { event: ActivityEvent }) {
  useTranslation() // re-render on language change
  return <>{tx(`activity.events.${event.code}`, event.params)}</>
}
