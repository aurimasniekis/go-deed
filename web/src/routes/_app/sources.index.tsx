import { createFileRoute } from '@tanstack/react-router'
import { SourcesPage } from '@/features/sources/SourcesPage'

export const Route = createFileRoute('/_app/sources/')({
  component: SourcesPage,
})
