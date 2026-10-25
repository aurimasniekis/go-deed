import { lazy, Suspense } from 'react'

/**
 * TanStack devtools, development builds only. The dynamic imports sit behind `import.meta.env.DEV`, which Vite replaces
 * with `false` in production, so the modules are tree-shaken; scripts/check-dist.mjs fails the build if they leak.
 */
const QueryDevtools = import.meta.env.DEV
  ? lazy(() => import('@tanstack/react-query-devtools').then((m) => ({ default: m.ReactQueryDevtools })))
  : null

export function Devtools() {
  if (!QueryDevtools) return null
  return (
    <Suspense fallback={null}>
      <QueryDevtools buttonPosition="bottom-left" />
    </Suspense>
  )
}

export const RouterDevtools = import.meta.env.DEV
  ? lazy(() => import('@tanstack/react-router-devtools').then((m) => ({ default: m.TanStackRouterDevtools })))
  : null
