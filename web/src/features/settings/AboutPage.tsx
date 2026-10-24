import { Badge, Card, Group, SimpleGrid, Stack, Text, Title } from '@mantine/core'
import { useSystemInfo } from '@/api/auth'
import { Page } from '@/components/AppShell'
import { BrandMark } from '@/components/Brand'
import { Field, Mono, PageHeader } from '@/components/common'
import { ErrorState, LoadingRows } from '@/components/States'
import { useTitle } from '@/lib/appStore'
import { SHORTCUTS } from '@/components/ShortcutsModal'
import { Kbd } from '@/components/common'

export function AboutPage() {
  useTitle('About')
  const info = useSystemInfo()
  return (
    <Page>
      <PageHeader title="About Deed" description="Declarative infrastructure catalog and live relationship map." />
      {info.isPending ? (
        <LoadingRows rows={4} />
      ) : info.isError ? (
        <ErrorState error={info.error} what="system information" />
      ) : (
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          <Card>
            <Group gap="sm" mb="sm">
              <BrandMark size={36} />
              <div>
                <Title order={2} fz={18}>
                  Deed
                </Title>
                <Text size="sm" c="dimmed">
                  Powered by Go, Terraform and React Flow
                </Text>
              </div>
            </Group>
            <Field label="Version">
              <Mono>{info.data.version}</Mono>
            </Field>
            {info.data.commit && (
              <Field label="Commit">
                <Mono>{info.data.commit.slice(0, 12)}</Mono>
              </Field>
            )}
            <Field label="API version">
              <Mono>{info.data.api_version}</Mono>
            </Field>
            <Field label="Web UI">
              <Mono>{import.meta.env.MODE}</Mono>
            </Field>
            <Field label="Sign-in">
              {info.data.local_auth ? 'email and password' : 'password sign-in disabled'}
              {info.data.sso_providers.length
                ? ` · SSO: ${info.data.sso_providers.map((p) => p.display_name).join(', ')}`
                : ''}
            </Field>
            <Field label="Features">
              <Group gap={4}>
                {info.data.features.map((f) => (
                  <Badge key={f} size="sm" variant="light" color="gray" styles={{ label: { textTransform: 'none' } }}>
                    {f}
                  </Badge>
                ))}
              </Group>
            </Field>
          </Card>
          <Card>
            <Title order={2} fz={14} mb="xs">
              Keyboard shortcuts
            </Title>
            <Stack gap={4}>
              {SHORTCUTS.flatMap((g) =>
                g.items.slice(0, 6).map(([keys, label]) => (
                  <Group key={g.group + label + keys.join()} justify="space-between">
                    <Text size="sm">
                      {label}{' '}
                      <Text span c="dimmed" size="xs">
                        ({g.group})
                      </Text>
                    </Text>
                    <Group gap={4}>
                      {keys.map((k) => (
                        <Kbd key={k}>{k}</Kbd>
                      ))}
                    </Group>
                  </Group>
                )),
              )}
            </Stack>
          </Card>
        </SimpleGrid>
      )}
    </Page>
  )
}
