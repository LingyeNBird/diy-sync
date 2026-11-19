import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'
import { Browser } from '@/features/browser'

export const Route = createFileRoute('/_authenticated/browse/')({
  validateSearch: z.object({ connection: z.string().optional(), binding: z.string().optional() }),
  component: function BrowsePage() {
    const { connection, binding } = Route.useSearch()
    return <Browser connectionId={connection} bindingId={binding} />
  },
})
