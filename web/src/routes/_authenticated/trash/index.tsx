import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'
import { Trash } from '@/features/trash'

export const Route = createFileRoute('/_authenticated/trash/')({
  validateSearch: z.object({ q: z.string().optional(), folder: z.string().optional() }),
  component: function TrashPage() {
    const { q, folder } = Route.useSearch()
    return <Trash key={`${folder}/${q}`} initialQuery={q} initialFolder={folder} />
  },
})
