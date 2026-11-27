import { createFileRoute } from '@tanstack/react-router'
import { Clipboard } from '@/features/clipboard'

export const Route = createFileRoute('/_authenticated/clipboard/')({
  component: Clipboard,
})
