import { createFileRoute } from '@tanstack/react-router'
import { NewKindPage } from '@/features/kinds/KindsPage'

export const Route = createFileRoute('/_app/kinds/new')({
  component: NewKindPage,
})
