import { createFileRoute } from '@tanstack/react-router'
import { RelTypesPage } from '@/features/kinds/RelTypesPage'

export const Route = createFileRoute('/_app/relationship-types')({
  component: RelTypesPage,
})
