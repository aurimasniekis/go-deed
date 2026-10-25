import { describe, expect, it } from 'vitest'
import { wrapLayers } from './layoutTypes'

const size = { width: 100, height: 40 }

describe('wrapLayers', () => {
  it('leaves short layers alone', () => {
    const positions = { a: { x: 0, y: 0 }, b: { x: 200, y: 0 }, c: { x: 200, y: 60 } }
    const sizes = new Map(Object.keys(positions).map((id) => [id, size]))
    expect(wrapLayers(positions, sizes, 'RIGHT', 5)).toEqual(positions)
  })

  it('wraps a tall layer into columns and shifts later layers', () => {
    const positions: Record<string, { x: number; y: number }> = { root: { x: 0, y: 0 }, tail: { x: 400, y: 0 } }
    for (let i = 0; i < 10; i++) positions[`n${String(i)}`] = { x: 200, y: i * 60 }
    const sizes = new Map(Object.keys(positions).map((id) => [id, size]))
    const out = wrapLayers(positions, sizes, 'RIGHT', 4)
    const xs = new Set(Array.from({ length: 10 }, (_, i) => out[`n${String(i)}`]?.x))
    expect(xs.size).toBe(3) // 10 nodes, 4 per column
    const ys = Array.from({ length: 10 }, (_, i) => out[`n${String(i)}`]?.y ?? 0)
    expect(Math.max(...ys) - Math.min(...ys)).toBeLessThan(4 * 66)
    expect(out.root).toEqual(positions.root)
    // The following layer moves right past the extra columns.
    expect(out.tail?.x).toBeGreaterThan(Math.max(...[...xs].map((x) => x ?? 0)))
  })

  it('wraps vertical layouts into rows', () => {
    const positions: Record<string, { x: number; y: number }> = {}
    for (let i = 0; i < 6; i++) positions[`n${String(i)}`] = { x: i * 120, y: 100 }
    const sizes = new Map(Object.keys(positions).map((id) => [id, size]))
    const out = wrapLayers(positions, sizes, 'DOWN', 3)
    expect(new Set(Object.values(out).map((p) => p.y)).size).toBe(2)
  })
})
