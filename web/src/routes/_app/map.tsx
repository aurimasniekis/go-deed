import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { MapPage } from '@/features/map/MapPage'
import { validateMapSearch, type MapSearch } from '@/lib/urlState'

export const Route = createFileRoute('/_app/map')({
  validateSearch: (s: Record<string, unknown>): MapSearch => validateMapSearch(s),
  component: MapRoute,
})

function MapRoute() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  return <MapPage search={search} setSearch={(s, replace) => void navigate({ search: s, replace: replace ?? false })} />
}
