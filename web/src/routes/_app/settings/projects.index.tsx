import { createFileRoute } from '@tanstack/react-router'
import { ProjectsPage } from '@/features/settings/ProjectsPage'

export const Route = createFileRoute('/_app/settings/projects/')({
  component: ProjectsPage,
})
