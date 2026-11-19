import { createFileRoute, redirect } from '@tanstack/react-router'
import { getSession } from '@/api/client'
import { overviewQuery } from '@/api/data'
import { AuthenticatedLayout } from '@/components/layout/authenticated-layout'

export const Route = createFileRoute('/_authenticated')({
  // Signed in? Then load the first overview, so pages always have data.
  beforeLoad: async ({ context, location }) => {
    const s = await getSession()
    if (!s.authenticated) throw redirect({ to: '/sign-in', search: { redirect: location.href } })
    await context.queryClient.ensureQueryData(overviewQuery)
  },
  component: AuthenticatedLayout,
})
