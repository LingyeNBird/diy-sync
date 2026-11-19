import { createFileRoute } from '@tanstack/react-router'
import { Folders } from '@/features/folders'

export const Route = createFileRoute('/_authenticated/folders/')({ component: Folders })
