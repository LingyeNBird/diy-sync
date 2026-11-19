import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useLayout } from '@/context/layout-provider'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
} from '@/components/ui/sidebar'
import { Logo } from '@/assets/logo'
import { cn } from '@/lib/utils'
import { useData, useOverview } from '@/api/data'
import { sidebarData } from './data/sidebar-data'
import { NavGroup } from './nav-group'
import { NavUser } from './nav-user'

export function useNavGroups() {
  const { t } = useTranslation()
  const { conflicts, pairing } = useData()
  const conflictCount = conflicts.length
  const pairingCount = pairing.length
  const badges: Record<string, number> = { 'nav.conflicts': conflictCount, 'nav.pairing': pairingCount }
  return sidebarData.navGroups.map((g) => ({
    ...g,
    title: t(g.title as never),
    items: g.items.map((i) => ({
      ...i,
      title: t(i.title as never),
      badge: badges[i.title] ? String(badges[i.title]) : undefined,
    })),
  })) as typeof sidebarData.navGroups
}

export function AppSidebar() {
  const { t } = useTranslation()
  const { collapsible, variant } = useLayout()
  const { setOpenMobile } = useSidebar()
  const navGroups = useNavGroups()
  const overview = useOverview()
  const down = overview.isError
  return (
    <Sidebar collapsible={collapsible} variant={variant}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size='lg' asChild>
              <Link to='/' onClick={() => setOpenMobile(false)}>
                <div className='flex aspect-square size-8 items-center justify-center'>
                  <Logo className='size-8' />
                </div>
                <div className='grid flex-1 text-start text-sm leading-tight'>
                  <span className='truncate font-semibold'>fdsync</span>
                  <span className='flex items-center gap-1.5 truncate text-xs text-muted-foreground'>
                    <span className={cn('size-1.5 rounded-full', down ? 'bg-rose-500' : 'bg-emerald-500')} />
                    {down ? t('sidebar.unreachable') : t('sidebar.running', { version: overview.data?.server.version ?? '' })}
                  </span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        {navGroups.map((props) => (
          <NavGroup key={props.title} {...props} />
        ))}
      </SidebarContent>
      <SidebarFooter>
        <NavUser />
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
