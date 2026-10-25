import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { SetupPage } from '@/features/auth/SetupPage'

export const Route = createFileRoute('/setup')({
  component: Setup,
})

function Setup() {
  const navigate = useNavigate()
  return <SetupPage onDone={() => void navigate({ to: '/', replace: true })} />
}
