import { createFileRoute } from '@tanstack/react-router'
import { TelemetryPage } from '@/features/settings/TelemetryPage'

export const Route = createFileRoute('/_app/settings/telemetry')({
  component: TelemetryPage,
})
