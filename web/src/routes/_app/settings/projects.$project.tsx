import { createFileRoute } from '@tanstack/react-router'
import { ProjectDetailPage } from '@/features/settings/ProjectsPage'

export const Route = createFileRoute('/_app/settings/projects/$project')({
  component: ProjectDetail,
})

function ProjectDetail() {
  const { project } = Route.useParams()
  return <ProjectDetailPage key={project} project={project} />
}
