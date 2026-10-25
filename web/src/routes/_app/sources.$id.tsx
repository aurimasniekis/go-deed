import { createFileRoute } from '@tanstack/react-router'
import { SourceDetailPage } from '@/features/sources/SourceDetailPage'

export const Route = createFileRoute('/_app/sources/$id')({
  component: SourceDetail,
})

function SourceDetail() {
  const { id } = Route.useParams()
  return <SourceDetailPage key={id} id={id} />
}
