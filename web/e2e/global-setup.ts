import { mkdirSync, writeFileSync } from 'node:fs'
import { E2E_BASE_URL, E2E_PORT, MOCKS_PORT, OWNER_STATE, STATE_DIR, STORAGE_STATE } from './paths'
import { startMockUpstreams } from './mock-upstreams.ts'
import { bootstrapOwner, seedCatalog, signIn } from './seed.ts'
import { startServer } from './server.ts'

/**
 * One server and one bootstrap per run: start mock upstreams (Prometheus, GitHub) and `deed serve`, create the owner
 * with TOTP, seed the catalog, save a signed-in browser session and wait for the first findings evaluation.
 */
export default async function globalSetup() {
  mkdirSync(STATE_DIR, { recursive: true, mode: 0o700 })
  const mocks = await startMockUpstreams(MOCKS_PORT)
  const srv = await startServer({
    port: E2E_PORT,
    binary: process.env.DEED_E2E_BINARY,
    logLevel: process.env.DEED_E2E_LOG ?? 'warn',
  })
  if (srv.baseURL !== E2E_BASE_URL) throw new Error(`unexpected base URL ${srv.baseURL}`)
  try {
    const { client, state } = await bootstrapOwner(srv.baseURL, srv.setupLink)
    const seed = await seedCatalog(client, {
      prometheusURL: `http://127.0.0.1:${MOCKS_PORT}`,
      large: Number(process.env.DEED_E2E_LARGE ?? '0'),
    })
    const session = await signIn(state)
    writeFileSync(
      STORAGE_STATE,
      JSON.stringify({
        cookies: session.cookieList().map((c) => ({
          ...c,
          domain: '127.0.0.1',
          path: '/',
          expires: -1,
          httpOnly: true,
          secure: false,
          sameSite: 'Lax',
        })),
        origins: [],
      }),
      { mode: 0o600 },
    )
    // Findings are evaluated every two minutes; the flows below start from them.
    const deadline = Date.now() + 180_000
    for (;;) {
      const f = await session.get<unknown[]>('/findings?limit=5')
      if (f.length > 0) break
      if (Date.now() > deadline) throw new Error('no findings after 3 minutes')
      await new Promise((r) => setTimeout(r, 2000))
    }
    writeFileSync(OWNER_STATE, JSON.stringify({ ...state, seed, mocks: `http://127.0.0.1:${MOCKS_PORT}` }, null, 2), {
      mode: 0o600,
    })
  } catch (e) {
    await srv.stop()
    mocks.close()
    throw e
  }
  return async () => {
    await srv.stop()
    mocks.close()
  }
}
