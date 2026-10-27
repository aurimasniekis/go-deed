import { describe, expect, it } from 'vitest'
import { formatMetric } from './useNodeMetrics'

describe('formatMetric', () => {
  it('renders percentages and ratios', () => {
    expect(formatMetric(23.4, 'percent')).toBe('23.4%')
    expect(formatMetric(61, '%')).toBe('61%')
    expect(formatMetric(0.615, 'ratio')).toBe('61.5%')
    expect(formatMetric(1, 'ratio')).toBe('100%')
  })

  it('scales bytes by powers of 1024', () => {
    expect(formatMetric(512, 'bytes')).toBe('512 B')
    expect(formatMetric(1536, 'bytes')).toBe('1.5 KiB')
    expect(formatMetric(3 * 1024 ** 3, 'bytes')).toBe('3 GiB')
  })

  it('keeps other units and trims trailing zeros', () => {
    expect(formatMetric(0.4, 'req/s')).toBe('0.4 req/s')
    expect(formatMetric(12, 'ms')).toBe('12 ms')
    expect(formatMetric(5)).toBe('5')
    expect(formatMetric(0)).toBe('0')
    expect(formatMetric(1234.5)).toBe('1235')
  })
})
