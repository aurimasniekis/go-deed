import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { RelationsPage, validateRelationsSearch, type RelationsSearch } from '@/features/relations/RelationsPage'

export const Route = createFileRoute('/_app/relations')({
  validateSearch: (s: Record<string, unknown>): RelationsSearch => validateRelationsSearch(s),
  component: Relations,
})

function Relations() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  return <RelationsPage search={search} setSearch={(s) => void navigate({ search: s, replace: true })} />
}
