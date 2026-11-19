import { formatDistanceToNowStrict, format } from 'date-fns'
import { enUS, zhCN } from 'date-fns/locale'
import i18n from '@/i18n'

const isZh = () => i18n.language === 'zh-CN'

export function formatBytes(n: number, digits = 1): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 100 ? 0 : digits)} ${units[i]}`
}

export function formatRate(bps: number): string {
  return bps === 0 ? '—' : `${formatBytes(bps)}/s`
}

export function formatCount(n: number): string {
  return n.toLocaleString(isZh() ? 'zh-CN' : 'en-US')
}

export function ago(iso: string): string {
  const d = new Date(iso)
  if (Math.abs(Date.now() - d.getTime()) < 45_000) return i18n.t('time.justNow')
  return formatDistanceToNowStrict(d, { addSuffix: true, locale: isZh() ? zhCN : enUS })
}

export function timestamp(iso: string): string {
  return format(new Date(iso), 'yyyy-MM-dd HH:mm:ss')
}

export function clock(iso: string): string {
  return format(new Date(iso), 'HH:mm:ss')
}
