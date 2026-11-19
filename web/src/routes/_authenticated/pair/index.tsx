import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'
import { Pair } from '@/features/pair'

export const Route = createFileRoute('/_authenticated/pair/')({
  validateSearch: z.object({ code: z.string().optional() }),
  component: function PairPage() {
    const { code } = Route.useSearch()
    return <Pair code={code} />
  },
})
