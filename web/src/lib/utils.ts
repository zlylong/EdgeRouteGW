import { type ClassValue, clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/** Formats a byte count for humans. */
export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n < 10 && i > 0 && !Number.isInteger(n) ? n.toFixed(1) : Math.round(n)} ${units[i]}`
}

/** Relative time in Chinese: "刚刚", "3 分钟前", or an absolute date when old. */
export function fmtAgo(iso: string | Date | undefined | null): string {
  if (!iso) return '-'
  const t = typeof iso === 'string' ? new Date(iso).getTime() : iso.getTime()
  if (Number.isNaN(t)) return '-'
  const s = Math.round((Date.now() - t) / 1000)
  if (s < 5) return '刚刚'
  if (s < 60) return `${s} 秒前`
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  if (s < 7 * 86400) return `${Math.floor(s / 86400)} 天前`
  return new Date(t).toLocaleDateString('zh-CN')
}

export function fmtTime(iso: string | undefined | null): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '-'
  return d.toLocaleString('zh-CN', { hour12: false })
}

export function shortId(id: string, n = 8): string {
  return id.length > n ? id.slice(0, n) : id
}
