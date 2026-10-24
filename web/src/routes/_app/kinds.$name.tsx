import { createFileRoute } from '@tanstack/react-router'
import { KindDetailPage } from '@/features/kinds/KindsPage'

export const Route = createFileRoute('/_app/kinds/$name')({
  component: KindDetail,
})

function KindDetail() {
  const { name } = Route.useParams()
  return <KindDetailPage key={name} name={name} />
}
