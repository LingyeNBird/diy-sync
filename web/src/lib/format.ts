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

/** Reports whether semver a (X.Y.Z) is strictly newer than b. */
export function isNewerVersion(a?: string, b?: string): boolean {
  const parse = (v?: string) => {
    const m = /^v?(\d+)\.(\d+)\.(\d+)$/.exec((v ?? '').trim())
    return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null
  }
  const pa = parse(a)
  if (!pa) return false // dev/unknown server build doesn't nag clients
  const pb = parse(b)
  if (!pb) return true
  for (let i = 0; i < 3; i++) {
    if (pa[i] !== pb[i]) return pa[i] > pb[i]
  }
  return false
}
