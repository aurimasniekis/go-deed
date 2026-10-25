/**
 * Starts a throwaway Deed server for e2e tests and local development: a fresh data directory (mode 0700), a
 * development-mode config, `deed admin bootstrap` for the first-owner link, then `deed serve`.
 *
 *   node e2e/server.ts --port 38787 [--dir /tmp/deed-e2e] [--seed] [--large 2000] [--mocks 39090] [--binary ../bin/deed]
 *
 * With --seed the catalog is seeded (e2e/seed.ts) and the owner's credentials are written to <dir>/owner.json.
 */
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

export interface RunningServer {
  baseURL: string
  dir: string
  setupLink: string
  process: ChildProcess
  stop: () => Promise<void>
}

const repoRoot = resolve(import.meta.dirname, '..', '..')

/** Builds bin/deed (embedding whatever is in internal/webui/dist) unless a binary is given. */
export function ensureBinary(binary?: string): string {
  if (binary) return resolve(binary)
  const out = join(repoRoot, 'bin', 'deed')
  execFileSync('go', ['build', '-o', out, './cmd/deed'], { cwd: repoRoot, stdio: 'inherit' })
  return out
}

export async function startServer(opts: {
  port: number
  dir?: string
  binary?: string
  logLevel?: string
}): Promise<RunningServer> {
  const binary = ensureBinary(opts.binary)
  const dir = opts.dir ?? mkdtempSync(join(tmpdir(), 'deed-e2e-'))
  rmSync(join(dir, 'data'), { recursive: true, force: true })
  mkdirSync(join(dir, 'data'), { recursive: true, mode: 0o700 })
  const baseURL = `http://127.0.0.1:${opts.port}`
  const cfg = join(dir, 'deed.yaml')
  writeFileSync(
    cfg,
    [
      'mode: development',
      `data_dir: ${join(dir, 'data')}`,
      `public_url: ${baseURL}`,
      'http:',
      `  listen: 127.0.0.1:${opts.port}`,
      'management:',
      `  listen: 127.0.0.1:${opts.port + 1000}`,
      'log:',
      `  level: ${opts.logLevel ?? 'warn'}`,
      '  format: text',
      'auth:',
      '  local:',
      '    enabled: true',
      '',
    ].join('\n'),
  )
  // deed refuses unknown DEED_* environment overrides, so the test harness's own variables are not passed on.
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('DEED_')))
  const boot = execFileSync(binary, ['admin', 'bootstrap', '-config', cfg], { encoding: 'utf8', env })
  const setupLink = /(https?:\/\/\S+\/setup#bootstrap=\S+)/.exec(boot)?.[1]
  if (!setupLink) throw new Error(`no setup link in bootstrap output:\n${boot}`)
  const proc = spawn(binary, ['serve', '-config', cfg], { stdio: ['ignore', 'inherit', 'inherit'], env })
  const deadline = Date.now() + 30_000
  for (;;) {
    try {
      const r = await fetch(`${baseURL}/api/v1/system/info`)
      if (r.ok) break
    } catch {
      // not listening yet
    }
    if (proc.exitCode !== null) throw new Error(`deed exited with ${proc.exitCode}`)
    if (Date.now() > deadline) throw new Error('deed did not start within 30 s')
    await new Promise((r) => setTimeout(r, 200))
  }
  return {
    baseURL,
    dir,
    setupLink,
    process: proc,
    stop: async () => {
      if (proc.exitCode !== null) return
      proc.kill('SIGTERM')
      await new Promise((r) => proc.once('exit', r))
    },
  }
}

async function main() {
  const args = process.argv.slice(2)
  const arg = (name: string) => {
    const i = args.indexOf(`--${name}`)
    return i >= 0 ? args[i + 1] : undefined
  }
  const port = Number(arg('port') ?? '38787')
  const dir = arg('dir')
  if (dir && !existsSync(dir)) mkdirSync(dir, { recursive: true, mode: 0o700 })
  const srv = await startServer({ port, dir, binary: arg('binary'), logLevel: arg('log') ?? 'info' })
  console.log(`deed listening on ${srv.baseURL} (data in ${srv.dir})`)
  if (args.includes('--seed')) {
    try {
      const { bootstrapOwner, seedCatalog } = await import('./seed.ts')
      const { client, state } = await bootstrapOwner(srv.baseURL, srv.setupLink)
      let prometheusURL: string | undefined
      const promPort = arg('mocks')
      if (promPort) {
        const { startMockUpstreams } = await import('./mock-upstreams.ts')
        await startMockUpstreams(Number(promPort))
        prometheusURL = `http://127.0.0.1:${promPort}`
      }
      const seed = await seedCatalog(client, {
        large: Number(arg('large') ?? '0'),
        ...(prometheusURL ? { prometheusURL } : {}),
      })
      writeFileSync(join(srv.dir, 'owner.json'), JSON.stringify({ ...state, seed }, null, 2), { mode: 0o600 })
      console.log(`seeded; owner ${state.email} / "${state.password}", TOTP secret ${state.totpSecret}`)
    } catch (e) {
      await srv.stop()
      throw e
    }
  } else {
    console.log(`first-owner link: ${srv.setupLink}`)
  }
  const stop = () => void srv.stop().then(() => process.exit(0))
  process.on('SIGINT', stop)
  process.on('SIGTERM', stop)
}

if (process.argv[1]?.endsWith('server.ts')) {
  main().catch((e: unknown) => {
    console.error(e)
    process.exit(1)
  })
}
