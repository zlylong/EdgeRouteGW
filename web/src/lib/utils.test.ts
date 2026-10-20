import { describe, expect, it } from 'vitest'
import { cn, fmtAgo, fmtBytes, shortId } from './utils'

describe('utils', () => {
  it('merges tailwind classes with later wins', () => {
    expect(cn('p-2', 'p-4', 'text-sm')).toBe('p-4 text-sm')
  })
  it('formats bytes', () => {
    expect(fmtBytes(0)).toBe('0 B')
    expect(fmtBytes(1536)).toBe('1.5 KB')
    expect(fmtBytes(5 * 1024 * 1024)).toBe('5 MB')
    expect(fmtBytes(-1)).toBe('-')
  })
  it('formats relative time', () => {
    expect(fmtAgo(new Date())).toBe('刚刚')
    expect(fmtAgo(new Date(Date.now() - 120_000))).toBe('2 分钟前')
    expect(fmtAgo(undefined)).toBe('-')
  })
  it('shortens ids', () => {
    expect(shortId('abcdefghijkl')).toBe('abcdefgh')
  })
})
