import {
  Activity,
  Columns2,
  FolderSync,
  GitCompareArrows,
  LayoutDashboard,
  MonitorSmartphone,
  Settings,
  Trash2,
  ShieldCheck,
} from 'lucide-react'
import { type SidebarData } from '../types'

// Titles are i18n keys; AppSidebar and the command menu translate them.
export const sidebarData: SidebarData = {
  navGroups: [
    {
      title: 'nav.overview',
      items: [
        { title: 'nav.dashboard', url: '/', icon: LayoutDashboard },
        { title: 'nav.conflicts', url: '/conflicts', icon: GitCompareArrows },
        { title: 'nav.activity', url: '/activity', icon: Activity },
      ],
    },
    {
      title: 'nav.sync',
      items: [
        { title: 'nav.folders', url: '/folders', icon: FolderSync },
        { title: 'nav.connections', url: '/connections', icon: MonitorSmartphone },
        { title: 'nav.browser', url: '/browse', icon: Columns2 },
        { title: 'nav.trash', url: '/trash', icon: Trash2 },
        { title: 'nav.pairing', url: '/pair', icon: ShieldCheck },
      ],
    },
    {
      title: 'nav.server',
      items: [{ title: 'nav.settings', url: '/settings', icon: Settings }],
    },
  ],
}
