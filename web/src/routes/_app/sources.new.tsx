import { Loader } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { api, unwrap, type Schemas } from '@/api/client'
import { useProjects, useProviders } from '@/api/queries'
import { Page } from '@/components/AppShell'
import { PageHeader } from '@/components/common'
import { ErrorState } from '@/components/States'
import { SourceForm } from '@/features/sources/SourceForm'
import { useTitle } from '@/lib/appStore'
import { compact, decodeString } from '@/lib/urlState'

export const Route = createFileRoute('/_app/sources/new')({
  validateSearch: (s: Record<string, unknown>): { provider?: string } =>
    compact({ provider: decodeString(s.provider, 64) }),
  component: NewSource,
})

function NewSource() {
  useTitle('New source')
  const { provider } = Route.useSearch()
  const providers = useProviders()
  const projects = useProjects()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const create = useMutation({
    mutationFn: async (body: Schemas['SourceBody']) => unwrap(await api.POST('/sources', { body })),
    onSuccess: (s) => {
      void qc.invalidateQueries({ queryKey: ['sources'] })
      notifications.show({ color: 'green', message: `Source ${s.name} created` })
      void navigate({ to: '/sources/$id', params: { id: s.id } })
    },
  })
  return (
    <Page>
      <PageHeader
        title="New source"
        description="Connect a system Deed imports from, or create a push source for an agent or job."
      />
      {providers.isPending || projects.isPending ? (
        <Loader size="sm" />
      ) : providers.isError ? (
        <ErrorState error={providers.error} what="providers" />
      ) : (
        <SourceForm
          providers={providers.data}
          initialProvider={provider}
          pending={create.isPending}
          error={create.error}
          onSubmit={(b) => create.mutate(b)}
          onCancel={() => void navigate({ to: '/sources' })}
        />
      )}
    </Page>
  )
}
