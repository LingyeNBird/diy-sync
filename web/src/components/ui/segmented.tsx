// Compact segmented control (a row of toggle buttons, one or more pressed).
import { cn } from '@/lib/utils'

export function Segmented({ children }: { children: React.ReactNode }) {
  return <div className='inline-flex h-8 items-center rounded-md border bg-muted/40 p-0.5'>{children}</div>
}

export function SegButton({
  on,
  disabled,
  onClick,
  children,
}: {
  on: boolean
  disabled?: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type='button'
      aria-pressed={on}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'inline-flex h-full items-center gap-1.5 rounded px-2.5 text-xs font-medium whitespace-nowrap text-muted-foreground transition-colors [&_svg]:size-3.5',
        'hover:text-foreground disabled:pointer-events-none disabled:opacity-40',
        on && 'bg-background text-foreground shadow-sm'
      )}
    >
      {children}
    </button>
  )
}
