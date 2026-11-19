import { createFileRoute } from '@tanstack/react-router'
import { FolderDetail } from '@/features/folders/detail'

export const Route = createFileRoute('/_authenticated/folders/$folderId')({
  component: function FolderPage() {
    const { folderId } = Route.useParams()
    return <FolderDetail key={folderId} folderId={folderId} />
  },
})
