import { createFileRoute } from '@tanstack/react-router'
import { ConnectionDetail } from '@/features/connections/detail'

export const Route = createFileRoute('/_authenticated/connections/$connectionId')({
  component: function ConnectionPage() {
    const { connectionId } = Route.useParams()
    return <ConnectionDetail key={connectionId} connectionId={connectionId} />
  },
})
