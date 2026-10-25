import {
  Alert,
  Badge,
  Button,
  Card,
  Code,
  FileButton,
  Group,
  Modal,
  SimpleGrid,
  Stack,
  Text,
  Textarea,
  TextInput,
  Tooltip,
} from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { IconPhoto, IconTrash, IconUpload } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { isInstallationAdmin, useMe } from '@/api/auth'
import { api, check, errorMessage, unwrap } from '@/api/client'
import { customIconsQuery, type CustomIcon } from '@/api/icons'
import { Page } from '@/components/AppShell'
import { ConfirmModal, CopyIcon, PageHeader, TimeAgo } from '@/components/common'
import { RefIcon } from '@/components/icons'
import { EmptyState, ErrorState, LoadingRows } from '@/components/States'
import { useTitle } from '@/lib/appStore'

const NAME_RE = /^[a-z0-9][a-z0-9._-]{0,127}$/
const MAX_BYTES = 64 * 1024

function nameFromFile(file: string): string {
  return file
    .toLowerCase()
    .replace(/\.svg$/, '')
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^[^a-z0-9]+/, '')
    .slice(0, 128)
}

function UploadModal({ opened, onClose, replace }: { opened: boolean; onClose: () => void; replace?: string }) {
  return (
    <Modal opened={opened} onClose={onClose} title={replace ? `Replace ${replace}` : 'Upload an icon'} size="lg">
      {opened && <UploadForm key={replace ?? ''} replace={replace} onClose={onClose} />}
    </Modal>
  )
}

function UploadForm({ onClose, replace }: { onClose: () => void; replace?: string }) {
  const qc = useQueryClient()
  const [name, setName] = useState(replace ?? '')
  const [svg, setSvg] = useState('')
  const nameError = name && !NAME_RE.test(name) ? 'Lowercase letters, digits, dot, dash and underscore' : null
  const tooBig = new Blob([svg]).size > MAX_BYTES
  const put = useMutation({
    mutationFn: async () => unwrap(await api.PUT('/icons/{name}', { params: { path: { name } }, body: { svg } })),
    onSuccess: (icon) => {
      notifications.show({
        color: 'green',
        title: replace ? 'Icon replaced' : 'Icon uploaded',
        message: `Use ${icon.ref} as a kind or entity icon.`,
      })
      void qc.invalidateQueries({ queryKey: ['icons'] })
      onClose()
    },
  })
  return (
    <Stack gap="sm">
      <Text size="sm" c="dimmed">
        SVG only, up to 64 KiB. The server removes scripts, styles, event handlers, foreignObject and external
        references. Icons painted only in black or <Code>currentColor</Code> are tinted to match the theme.
      </Text>
      <Group align="flex-end" gap="sm">
        <TextInput
          label="Name"
          description={name ? `Referenced as custom:${name}` : 'Referenced as custom:<name>'}
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          error={nameError}
          disabled={!!replace}
          required
          style={{ flex: 1 }}
          data-testid="icon-name"
        />
        <FileButton
          accept="image/svg+xml,.svg"
          onChange={(f) => {
            if (!f) return
            if (!name && !replace) setName(nameFromFile(f.name))
            void f.text().then(setSvg)
          }}
        >
          {(props) => (
            <Button variant="default" leftSection={<IconUpload size={14} />} {...props}>
              Choose file…
            </Button>
          )}
        </FileButton>
      </Group>
      <Textarea
        label="SVG source"
        value={svg}
        onChange={(e) => setSvg(e.currentTarget.value)}
        autosize
        minRows={4}
        maxRows={12}
        error={tooBig ? 'Larger than 64 KiB' : null}
        styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 11 } }}
        data-testid="icon-svg"
      />
      {put.isError && (
        <Alert color="red" title="The server rejected the icon">
          {errorMessage(put.error)}
        </Alert>
      )}
      <Group justify="flex-end">
        <Button variant="default" onClick={onClose}>
          Cancel
        </Button>
        <Button
          onClick={() => put.mutate()}
          loading={put.isPending}
          disabled={!name || !!nameError || !svg.trim() || tooBig}
          data-testid="icon-save"
        >
          {replace ? 'Replace' : 'Upload'}
        </Button>
      </Group>
    </Stack>
  )
}

/** Custom SVG icons for kinds and entities (owner/admin manage; everyone sees them). */
export function IconsPage() {
  useTitle('Icons')
  const qc = useQueryClient()
  const admin = isInstallationAdmin(useMe().data)
  const q = useQuery(customIconsQuery)
  const [upload, setUpload] = useState<{ replace?: string } | null>(null)
  const [deleting, setDeleting] = useState<CustomIcon | null>(null)
  const del = useMutation({
    mutationFn: async (name: string) => {
      check(await api.DELETE('/icons/{name}', { params: { path: { name } } }))
    },
    onSuccess: () => {
      setDeleting(null)
      void qc.invalidateQueries({ queryKey: ['icons'] })
    },
    onError: (e) => notifications.show({ color: 'red', title: 'Could not delete the icon', message: errorMessage(e) }),
  })
  return (
    <Page>
      <PageHeader
        title="Icons"
        description="Custom SVG icons for kinds and entities, referenced as custom:<name>. Built-in Tabler and brand icons need no upload."
        actions={
          admin && (
            <Button leftSection={<IconUpload size={14} />} onClick={() => setUpload({})} data-testid="upload-icon">
              Upload icon
            </Button>
          )
        }
      />
      {q.isPending ? (
        <LoadingRows />
      ) : q.isError ? (
        <ErrorState error={q.error} what="icons" onRetry={() => void q.refetch()} />
      ) : q.data.length === 0 ? (
        <EmptyState title="No custom icons" icon={<IconPhoto size={22} />}>
          {admin
            ? 'Upload an SVG to use it as a kind or entity icon.'
            : 'An installation owner or admin can upload SVG icons.'}
        </EmptyState>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="sm">
          {q.data.map((i) => (
            <Card key={i.id} data-testid={`icon-${i.name}`}>
              <Group wrap="nowrap" align="flex-start" gap="sm">
                <Group
                  gap={8}
                  p={8}
                  wrap="nowrap"
                  style={{ border: '1px solid var(--deed-border)', borderRadius: 4, flexShrink: 0 }}
                >
                  <RefIcon iconRef={i.ref} size={32} title={i.name} />
                  <RefIcon iconRef={i.ref} size={16} />
                </Group>
                <Stack gap={2} style={{ minWidth: 0, flex: 1 }}>
                  <Group gap={6} wrap="nowrap">
                    <Text fw={600} size="sm" truncate="end">
                      {i.name}
                    </Text>
                    {i.monochrome && (
                      <Tooltip label="Painted in one colour: tinted to match the text colour in both themes.">
                        <Badge size="xs" variant="light" color="gray">
                          tinted
                        </Badge>
                      </Tooltip>
                    )}
                  </Group>
                  <Group gap={4} wrap="nowrap">
                    <Code fz={11}>{i.ref}</Code>
                    <CopyIcon value={i.ref} label={`Copy ${i.ref}`} />
                  </Group>
                  <Text size="xs" c="dimmed">
                    {(i.bytes / 1024).toFixed(1)} KiB · <TimeAgo value={i.created_at} />
                  </Text>
                </Stack>
              </Group>
              {admin && (
                <Group gap="xs" mt="sm" justify="flex-end">
                  <Button size="compact-xs" variant="default" onClick={() => setUpload({ replace: i.name })}>
                    Replace
                  </Button>
                  <Button
                    size="compact-xs"
                    variant="light"
                    color="red"
                    leftSection={<IconTrash size={12} />}
                    onClick={() => setDeleting(i)}
                    aria-label={`Delete ${i.name}`}
                  >
                    Delete
                  </Button>
                </Group>
              )}
            </Card>
          ))}
        </SimpleGrid>
      )}
      <UploadModal opened={upload !== null} replace={upload?.replace} onClose={() => setUpload(null)} />
      <ConfirmModal
        opened={deleting !== null}
        title={`Delete ${deleting?.name ?? ''}?`}
        danger
        confirmLabel="Delete icon"
        pending={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting.name)}
        onClose={() => setDeleting(null)}
      >
        Kinds and entities that use <Code>{deleting?.ref}</Code> fall back to the default icon.
      </ConfirmModal>
    </Page>
  )
}
