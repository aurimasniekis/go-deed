/**
 * Per-user UI preferences in localStorage (project filter, column choices, map options). Never credentials or tokens.
 * Every access is guarded: storage may be unavailable (private windows, blocked site data) and the UI must still work.
 */

const PREFIX = 'deed.ui.'

export function readPref<T>(key: string, fallback: T, user = ''): T {
  try {
    const raw = window.localStorage.getItem(`${PREFIX}${user ? `${user}.` : ''}${key}`)
    if (raw === null) return fallback
    return JSON.parse(raw) as T
  } catch {
    return fallback
  }
}

export function writePref(key: string, value: unknown, user = ''): void {
  try {
    const k = `${PREFIX}${user ? `${user}.` : ''}${key}`
    if (value === undefined || value === null) window.localStorage.removeItem(k)
    else window.localStorage.setItem(k, JSON.stringify(value))
  } catch {
    // Preferences are a convenience; ignore storage failures.
  }
}
