import { cn } from '@/lib/utils'

export function Logo({ className, ...props }: React.SVGProps<SVGSVGElement>) {
  return (
    <svg
      viewBox='0 0 32 32'
      xmlns='http://www.w3.org/2000/svg'
      className={cn('size-6', className)}
      {...props}
    >
      <title>fdsync</title>
      <rect width='32' height='32' rx='7' className='fill-primary' />
      <path
        d='M9 12.5h11.5l-3-3M23 19.5H11.5l3 3'
        fill='none'
        stroke='#38bdf8'
        strokeWidth='2.6'
        strokeLinecap='round'
        strokeLinejoin='round'
      />
    </svg>
  )
}
