import type { QueryClient } from '@tanstack/react-query'
import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'
import { Suspense } from 'react'
import { RouterDevtools } from '@/devtools'
import { NotFoundPage, RouteErrorPage } from '@/components/ErrorPages'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: Root,
  notFoundComponent: NotFoundPage,
  errorComponent: RouteErrorPage,
})

function Root() {
  return (
    <>
      <Outlet />
      {RouterDevtools && (
        <Suspense fallback={null}>
          <RouterDevtools position="bottom-right" />
        </Suspense>
      )}
    </>
  )
}
