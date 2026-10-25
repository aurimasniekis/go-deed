import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { VersionsPage } from '@/features/versions/VersionsPage'
import { validateVersionsSearch, type VersionsSearch } from '@/lib/urlState'

export const Route = createFileRoute('/_app/versions')({
  validateSearch: (s: Record<string, unknown>): VersionsSearch => validateVersionsSearch(s),
  component: Versions,
})

function Versions() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  return <VersionsPage search={search} setSearch={(s) => void navigate({ search: s, replace: true })} />
}
