import { createHmac } from 'node:crypto'

/** RFC 4648 base32 (no padding required). */
export function base32Decode(input: string): Buffer {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  const clean = input.replace(/=+$/, '').replace(/\s+/g, '').toUpperCase()
  let bits = 0
  let value = 0
  const out: number[] = []
  for (const ch of clean) {
    const idx = alphabet.indexOf(ch)
    if (idx < 0) throw new Error(`invalid base32 character ${ch}`)
    value = (value << 5) | idx
    bits += 5
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 0xff)
      bits -= 8
    }
  }
  return Buffer.from(out)
}

/** RFC 6238 TOTP (SHA-1, 6 digits, 30 s), as Deed's authenticator enrollment specifies. */
export function totp(secret: string, step: number): string {
  const key = base32Decode(secret)
  const msg = Buffer.alloc(8)
  msg.writeBigUInt64BE(BigInt(step))
  const h = createHmac('sha1', key).update(msg).digest()
  const offset = (h[h.length - 1] ?? 0) & 0x0f
  const bin =
    (((h[offset] ?? 0) & 0x7f) << 24) |
    ((h[offset + 1] ?? 0) << 16) |
    ((h[offset + 2] ?? 0) << 8) |
    (h[offset + 3] ?? 0)
  return String(bin % 1_000_000).padStart(6, '0')
}

export function currentStep(now = Date.now()): number {
  return Math.floor(now / 1000 / 30)
}

/**
 * Hands out codes that the server has not seen yet. The server rejects a step at or below the last accepted one
 * (replay protection) and accepts ±1 step of skew, so each sign-in uses the next unused step and waits only when the
 * skew window is exhausted.
 */
export class TotpClock {
  private readonly secret: string
  private lastStep: number

  constructor(secret: string, lastStep = 0) {
    this.secret = secret
    this.lastStep = lastStep
  }

  get last(): number {
    return this.lastStep
  }

  async next(): Promise<string> {
    for (;;) {
      const cur = currentStep()
      const step = Math.max(cur - 1, this.lastStep + 1)
      if (step <= cur + 1) {
        this.lastStep = step
        return totp(this.secret, step)
      }
      await new Promise((r) => setTimeout(r, 1000))
    }
  }
}
