/**
 * Stable aliases for mask mode (screenshots and screen sharing): the same ID always gives the same alias, and the alias
 * reveals nothing about the name it replaces.
 */

const COMMON_ENVIRONMENTS = new Set([
  'production',
  'prod',
  'staging',
  'stage',
  'development',
  'dev',
  'test',
  'testing',
  'qa',
  'preview',
  'sandbox',
  'lab',
])

/** FNV-1a, base 36. */
function fnv(s: string): string {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return (h >>> 0).toString(36)
}

/** A prefix (a kind without its domain, `project`, `owner`, …) and four characters of the ID's hash. */
export function alias(prefix: string, id: string): string {
  const p = prefix.replace(/^.*\//, '').replace(/[^a-z0-9_-]/gi, '') || 'item'
  return `${p}-${fnv(id).padStart(4, '0').slice(-4)}`
}

/** Common environment names describe the shape, not the installation, and stay. */
export function maskEnvironment(env: string): string {
  return COMMON_ENVIRONMENTS.has(env.toLowerCase()) ? env : alias('env', env)
}

export const maskProject = (id: string) => alias('project', id)
