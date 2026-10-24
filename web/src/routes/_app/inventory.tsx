import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { InventoryPage } from '@/features/inventory/InventoryPage'
import { validateInventorySearch, type InventorySearch } from '@/lib/urlState'

export const Route = createFileRoute('/_app/inventory')({
  validateSearch: (s: Record<string, unknown>): InventorySearch => validateInventorySearch(s),
  component: Inventory,
})

function Inventory() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })
  return <InventoryPage search={search} setSearch={(s) => void navigate({ search: s, replace: true })} />
}
