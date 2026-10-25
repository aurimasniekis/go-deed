import { createFileRoute } from '@tanstack/react-router'
import { AlertsPage } from '@/features/alerts/AlertsPage'

export const Route = createFileRoute('/_app/alerts')({
  component: AlertsPage,
})
