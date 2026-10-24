import { describe, expect, it } from 'vitest'
import { parseSetupFragment } from '@/features/auth/sso'
import { csvCell, toCsv } from './csv'
import { duration, relativeTime } from './format'
import { safeLinkUrl } from './links'
import { safeNextPath } from './redirect'
import { isStaleRelation } from './status'

// Built at runtime so the source never contains a literal script URL.
const scriptUrl = ['javascript', 'alert(1)'].join(':')

describe('redirects', () => {
  const origin = 'https://deed.example'
  it('accepts same-origin paths only', () => {
    expect(safeNextPath('/map?focus=a', origin)).toBe('/map?focus=a')
    for (const bad of [
      'https://evil.example',
      '//evil.example',
      '/.//evil.example',
      '/%2e%2e//x',
      scriptUrl,
      '/login',
      '/api/v1/me',
      '\\\\evil',
    ]) {
      expect(safeNextPath(bad, origin)).toBe('/')
    }
  })
})

describe('links', () => {
  it('allows https only (http when the project allows it)', () => {
    expect(safeLinkUrl('https://github.com/x')).toBe('https://github.com/x')
    expect(safeLinkUrl('http://intranet/x')).toBeNull()
    expect(safeLinkUrl('http://intranet/x', true)).toBe('http://intranet/x')
    expect(safeLinkUrl(scriptUrl)).toBeNull()
    expect(safeLinkUrl('data:text/html,x')).toBeNull()
    expect(safeLinkUrl('https://user:pw@host/')).toBeNull()
    expect(safeLinkUrl(`https://x/${'a'.repeat(3000)}`)).toBeNull()
  })
})

describe('setup links', () => {
  it('reads the purpose and token from the fragment', () => {
    expect(parseSetupFragment('#bootstrap=deed_setup_abcdefgh123')).toEqual({
      purpose: 'bootstrap',
      token: 'deed_setup_abcdefgh123',
    })
    expect(parseSetupFragment('#invite=deed_setup_abcdefgh123')?.purpose).toBe('invite')
    expect(parseSetupFragment('#reset=x')).toBeNull()
    expect(parseSetupFragment('#other=deed_setup_abcdefgh123')).toBeNull()
    expect(parseSetupFragment('')).toBeNull()
  })
})

describe('CSV export', () => {
  it('quotes and neutralises formulas', () => {
    expect(csvCell('a,b')).toBe('"a,b"')
    expect(csvCell('say "hi"')).toBe('"say ""hi"""')
    expect(csvCell('=HYPERLINK("x")')).toBe(`"'=HYPERLINK(""x"")"`)
    expect(csvCell(null)).toBe('')
    expect(csvCell({ a: 1 })).toBe('"{""a"":1}"')
    expect(toCsv(['id', 'n'], [['1', 2]])).toBe('id,n\r\n1,2\r\n')
  })
})

describe('format', () => {
  it('formats relative times and durations', () => {
    const now = Date.parse('2026-10-25T12:00:00Z')
    expect(relativeTime('2026-10-25T11:57:00Z', now)).toBe('3 minutes ago')
    expect(relativeTime(undefined, now)).toBe('never')
    expect(relativeTime('0001-01-01T00:00:00Z', now)).toBe('never')
    expect(duration(3900)).toBe('1h 5m')
    expect(duration(45)).toBe('45s')
    expect(duration(0)).toBe('—')
  })
})

describe('relationship staleness', () => {
  it('only observed and inferred relationships go stale after 7 days', () => {
    const now = Date.parse('2026-10-25T00:00:00Z')
    expect(isStaleRelation('observed', '2026-10-10T00:00:00Z', now)).toBe(true)
    expect(isStaleRelation('observed', '2026-10-24T00:00:00Z', now)).toBe(false)
    expect(isStaleRelation('declared', '2020-01-01T00:00:00Z', now)).toBe(false)
    expect(isStaleRelation('inferred', undefined, now)).toBe(false)
  })
})
