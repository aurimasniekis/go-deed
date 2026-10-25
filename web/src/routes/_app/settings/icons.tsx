import { createFileRoute } from '@tanstack/react-router'
import { IconsPage } from '@/features/settings/IconsPage'

export const Route = createFileRoute('/_app/settings/icons')({
  component: IconsPage,
})
