import { createFileRoute } from '@tanstack/react-router'
import { KindsPage } from '@/features/kinds/KindsPage'

export const Route = createFileRoute('/_app/kinds/')({
  component: KindsPage,
})
