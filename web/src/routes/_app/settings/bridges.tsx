import { createFileRoute } from '@tanstack/react-router'
import { BridgesPage } from '@/features/settings/BridgesPage'

export const Route = createFileRoute('/_app/settings/bridges')({
  component: BridgesPage,
})
