import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'
import { Conflicts } from '@/features/conflicts'

export const Route = createFileRoute('/_authenticated/conflicts/')({
  validateSearch: z.object({ path: z.string().optional() }),
  component: function ConflictsPage() {
    const { path } = Route.useSearch()
    return <Conflicts key={path} initialPath={path} />
  },
})
