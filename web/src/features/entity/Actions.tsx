import {
  ActionIcon,
  Alert,
  Button,
  Group,
  Loader,
  Modal,
  Select,
  Stack,
  TagsInput,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core'
import { useDebouncedValue } from '@mantine/hooks'
import { notifications } from '@mantine/notifications'
import { IconPlus, IconTrash } from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, errorMessage, unwrap, type Declaration, type Entity } from '@/api/client'
import { keys } from '@/api/queries'
import { parseLabelTags } from '@/features/inventory/BulkActions'
import { safeLinkUrl } from '@/lib/links'
import { useDeclaration } from './queries'

const LINK_TYPES = ['repository', 'dashboard', 'logs', 'docs', 'runbook', 'metrics', 'alerts', 'ticket', 'other']

interface LinkRow {
  type: string
  title: string
  url: string
}

function useInvalidateEntity(id: string) {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: ['entities'] })
    void qc.invalidateQueries({ queryKey: ['graph'] })
    void qc.invalidateQueries({ queryKey: keys.entity(id) })
  }
}

/**
 * UI annotations: PUT /entities/{id}/declarations/ui (metadata only). Annotations rank below declarations, so the form
 * explains that a declared description/owner/environment keeps winning.
 */
export function AnnotateModal({
  e,
  opened,
  onClose,
  allowHttp,
}: {
  e: Entity
  opened: boolean
  onClose: () => void
  allowHttp: boolean
}) {
  const decl = useDeclaration(e.metadata.id, 'ui', opened)
  return (
    <Modal opened={opened} onClose={onClose} title="Edit UI annotations" size="lg" centered>
      {decl.isPending ? (
        <Loader size="sm" />
      ) : (
        <AnnotateForm e={e} decl={decl.data ?? null} onClose={onClose} allowHttp={allowHttp} />
      )}
    </Modal>
  )
}

function AnnotateForm({
  e,
  decl,
  onClose,
  allowHttp,
}: {
  e: Entity
  decl: Declaration | null
  onClose: () => void
  allowHttp: boolean
}) {
  const id = e.metadata.id
  const invalidate = useInvalidateEntity(id)
  const md = (decl?.metadata ?? {}) as Record<string, unknown>
  const [description, setDescription] = useState(typeof md.description === 'string' ? md.description : '')
  const [owner, setOwner] = useState(typeof md.owner === 'string' ? md.owner : '')
  const [environment, setEnvironment] = useState(typeof md.environment === 'string' ? md.environment : '')
  const [labels, setLabels] = useState<string[]>(() =>
    Object.entries((md.labels ?? {}) as Record<string, string>).map(([k, v]) => `${k}=${v}`),
  )
  const [links, setLinks] = useState<LinkRow[]>(() =>
    Array.isArray(md.links)
      ? (md.links as { type: string; title?: string; url: string }[]).map((x) => ({
          type: x.type,
          title: x.title ?? '',
          url: x.url,
        }))
      : [],
  )

  const parsedLabels = parseLabelTags(labels)
  const badLink = links.find((l) => l.url && !safeLinkUrl(l.url, allowHttp))
  const badEnv = environment !== '' && !/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(environment)

  const save = useMutation({
    mutationFn: async () => {
      const metadata: Record<string, unknown> = {}
      if (description.trim()) metadata.description = description.trim()
      if (owner.trim()) metadata.owner = owner.trim()
      if (environment.trim()) metadata.environment = environment.trim()
      if (parsedLabels && Object.keys(parsedLabels).length) metadata.labels = parsedLabels
      const ls = links
        .filter((l) => l.url.trim())
        .map((l) => ({ type: l.type, url: l.url.trim(), ...(l.title.trim() ? { title: l.title.trim() } : {}) }))
      if (ls.length) metadata.links = ls
      return unwrap(
        await api.PUT('/entities/{id}/declarations/{source}', {
          params: {
            path: { id, source: 'ui' },
            ...(decl ? { header: { 'If-Match': String(decl.resource_version) } } : {}),
          },
          body: { metadata },
        }),
      )
    },
    onSuccess: () => {
      invalidate()
      notifications.show({ color: 'green', message: 'Annotations saved' })
      onClose()
    },
  })

  return (
    <form
      onSubmit={(ev) => {
        ev.preventDefault()
        save.mutate()
      }}
    >
      <Stack gap="sm">
        <Alert color="blue" variant="light">
          Annotations are the UI’s own facts about this entity. They rank <b>below declarations</b> (Terraform, API) and
          above imports and discovery: if a declaration sets the same field, the declared value still wins. Labels and
          links merge per key with other sources.
        </Alert>
        <Textarea
          label="Description"
          autosize
          minRows={2}
          maxRows={8}
          maxLength={8192}
          value={description}
          onChange={(ev) => setDescription(ev.currentTarget.value)}
        />
        <Group grow>
          <TextInput
            label="Owner"
            description="Group name or ID"
            value={owner}
            onChange={(ev) => setOwner(ev.currentTarget.value)}
          />
          <TextInput
            label="Environment"
            value={environment}
            onChange={(ev) => setEnvironment(ev.currentTarget.value)}
            error={badEnv ? 'Lowercase slug' : undefined}
          />
        </Group>
        <TagsInput
          label="Labels"
          description="key=value"
          value={labels}
          onChange={setLabels}
          error={parsedLabels === null ? 'Each label must be key=value' : undefined}
          splitChars={[',']}
        />
        <Stack gap={4}>
          <Group justify="space-between">
            <Text size="sm" fw={500}>
              Links
            </Text>
            <Button
              size="compact-xs"
              variant="default"
              leftSection={<IconPlus size={12} />}
              onClick={() => setLinks([...links, { type: 'docs', title: '', url: '' }])}
            >
              Add link
            </Button>
          </Group>
          {links.map((l, i) => (
            <Group key={i} gap="xs" wrap="nowrap" align="flex-start">
              <Select
                aria-label="Link type"
                data={LINK_TYPES}
                value={l.type}
                onChange={(v) => setLinks(links.map((x, j) => (j === i ? { ...x, type: v ?? 'other' } : x)))}
                w={130}
                allowDeselect={false}
              />
              <TextInput
                aria-label="Link title"
                placeholder="Title"
                value={l.title}
                onChange={(ev) => {
                  const v = ev.currentTarget.value
                  setLinks(links.map((x, j) => (j === i ? { ...x, title: v } : x)))
                }}
                w={160}
              />
              <TextInput
                aria-label="Link URL"
                placeholder="https://…"
                value={l.url}
                onChange={(ev) => {
                  const v = ev.currentTarget.value
                  setLinks(links.map((x, j) => (j === i ? { ...x, url: v } : x)))
                }}
                error={
                  l.url && !safeLinkUrl(l.url, allowHttp)
                    ? allowHttp
                      ? 'http(s) URL required'
                      : 'https URL required'
                    : undefined
                }
                style={{ flex: 1 }}
              />
              <ActionIcon
                color="red"
                aria-label="Remove link"
                onClick={() => setLinks(links.filter((_, j) => j !== i))}
                mt={4}
              >
                <IconTrash size={14} />
              </ActionIcon>
            </Group>
          ))}
        </Stack>
        {save.isError && (
          <Alert color="red" role="alert">
            {errorMessage(save.error)}
          </Alert>
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            loading={save.isPending}
            disabled={parsedLabels === null || !!badLink || badEnv}
            data-testid="save-annotations"
          >
            Save annotations
          </Button>
        </Group>
      </Stack>
    </form>
  )
}

/** Declaring through the API source (editors): the declaration's metadata and spec as JSON, validated by the server. */
export function DeclareModal({ e, opened, onClose }: { e: Entity; opened: boolean; onClose: () => void }) {
  const decl = useDeclaration(e.metadata.id, 'api', opened)
  return (
    <Modal opened={opened} onClose={onClose} title="Declare through the API source" size="xl" centered>
      {decl.isPending ? <Loader size="sm" /> : <DeclareForm e={e} decl={decl.data ?? null} onClose={onClose} />}
    </Modal>
  )
}

function DeclareForm({ e, decl, onClose }: { e: Entity; decl: Declaration | null; onClose: () => void }) {
  const id = e.metadata.id
  const invalidate = useInvalidateEntity(id)
  const [text, setText] = useState(() =>
    JSON.stringify({ metadata: decl?.metadata ?? {}, spec: decl?.spec ?? {} }, null, 2),
  )
  const [parseError, setParseError] = useState('')

  const save = useMutation({
    mutationFn: async () => {
      let body: { metadata?: Record<string, unknown>; spec?: Record<string, unknown> }
      try {
        body = JSON.parse(text) as typeof body
      } catch (err) {
        setParseError((err as Error).message)
        throw new Error('The declaration is not valid JSON', { cause: err })
      }
      return unwrap(
        await api.PUT('/entities/{id}/declarations/{source}', {
          params: {
            path: { id, source: 'api' },
            ...(decl ? { header: { 'If-Match': String(decl.resource_version) } } : {}),
          },
          body,
        }),
      )
    },
    onSuccess: () => {
      invalidate()
      notifications.show({ color: 'green', message: 'Declaration saved' })
      onClose()
    },
  })

  return (
    <Stack gap="sm">
      <Text size="sm" c="dimmed">
        {decl
          ? `Editing the API source’s declaration (version ${decl.resource_version}).`
          : 'The API source has not declared this entity yet; saving creates its declaration.'}{' '}
        Declarations rank above imports, discovery and UI annotations. The spec is validated against the {e.kind} schema
        ({e.status.schemaVersion}); entities managed by Terraform should be changed in Terraform instead.
      </Text>
      <Textarea
        label="Declaration (JSON: metadata and spec)"
        autosize
        minRows={14}
        maxRows={28}
        value={text}
        onChange={(ev) => setText(ev.currentTarget.value)}
        styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
        error={parseError || undefined}
        spellCheck={false}
      />
      {save.isError && (
        <Alert color="red" role="alert">
          {errorMessage(save.error)}
        </Alert>
      )}
      <Group justify="flex-end">
        <Button variant="default" onClick={onClose}>
          Cancel
        </Button>
        <Button loading={save.isPending} onClick={() => save.mutate()}>
          Save declaration
        </Button>
      </Group>
    </Stack>
  )
}

export function RenameModal({ e, opened, onClose }: { e: Entity; opened: boolean; onClose: () => void }) {
  const [name, setName] = useState(e.metadata.name)
  const invalidate = useInvalidateEntity(e.metadata.id)
  const valid = /^[a-z0-9]([a-z0-9._-]{0,251}[a-z0-9])?$/.test(name)
  const save = useMutation({
    mutationFn: async () =>
      unwrap(await api.POST('/entities/{id}/rename', { params: { path: { id: e.metadata.id } }, body: { name } })),
    onSuccess: () => {
      invalidate()
      notifications.show({ color: 'green', message: `Renamed to ${name}` })
      onClose()
    },
  })
  return (
    <Modal opened={opened} onClose={onClose} title="Rename entity" centered>
      <form
        onSubmit={(ev) => {
          ev.preventDefault()
          save.mutate()
        }}
      >
        <Stack gap="sm">
          <Text size="sm" c="dimmed">
            The old name becomes an alias, so sources and links that use it keep resolving. The ID never changes.
          </Text>
          <TextInput
            label="New name"
            value={name}
            onChange={(ev) => setName(ev.currentTarget.value)}
            error={name && !valid ? 'Lowercase slug: a-z, 0-9, . _ -' : undefined}
            data-autofocus
          />
          {save.isError && (
            <Alert color="red" role="alert">
              {errorMessage(save.error)}
            </Alert>
          )}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" loading={save.isPending} disabled={!valid || name === e.metadata.name}>
              Rename
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  )
}

/** Merge a duplicate into this entity (it is tombstoned; its name becomes an alias). */
export function MergeModal({ e, opened, onClose }: { e: Entity; opened: boolean; onClose: () => void }) {
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [debounced] = useDebouncedValue(q, 200)
  const [from, setFrom] = useState<string | null>(null)
  const [confirm, setConfirm] = useState(false)
  const candidates = useQuery({
    queryKey: keys.entities({ merge: e.metadata.id, q: debounced }),
    queryFn: async () =>
      unwrap(
        await api.GET('/entities', {
          params: {
            query: { kind: [e.kind], project: [e.metadata.project], limit: 20, ...(debounced ? { q: debounced } : {}) },
          },
        }),
      ),
    enabled: opened,
  })
  const invalidate = useInvalidateEntity(e.metadata.id)
  const merge = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/entities/{id}/merge', { params: { path: { id: e.metadata.id } }, body: { from: from ?? '' } }),
      ),
    onSuccess: () => {
      invalidate()
      notifications.show({ color: 'green', message: 'Entities merged' })
      onClose()
      void navigate({ to: '/entities/$id', params: { id: e.metadata.id } })
    },
  })
  const options = (candidates.data?.items ?? [])
    .filter((x) => x.metadata.id !== e.metadata.id)
    .map((x) => ({
      value: x.metadata.id,
      label: `${x.metadata.name}${x.metadata.displayName ? ` (${x.metadata.displayName})` : ''}`,
    }))
  const chosen = options.find((o) => o.value === from)
  return (
    <Modal opened={opened} onClose={onClose} title="Merge a duplicate into this entity" size="lg" centered>
      <Stack gap="sm">
        <Text size="sm" c="dimmed">
          Pick the duplicate {e.kind} in {e.metadata.project}. Its facts move here, it is tombstoned and its name
          becomes an alias of <b>{e.metadata.name}</b>. This is audited and cannot be undone automatically.
        </Text>
        <Select
          label="Duplicate"
          searchable
          searchValue={q}
          onSearchChange={setQ}
          data={options}
          value={from}
          onChange={(v) => {
            setFrom(v)
            setConfirm(false)
          }}
          nothingFoundMessage={candidates.isFetching ? 'Searching…' : 'No other entity of this kind'}
          filter={({ options: o }) => o}
        />
        {merge.isError && (
          <Alert color="red" role="alert">
            {errorMessage(merge.error)}
          </Alert>
        )}
        {confirm ? (
          <Alert color="red" title="Confirm merge">
            <Stack gap="xs">
              <Text size="sm">
                Merge <b>{chosen?.label}</b> into <b>{e.metadata.name}</b>?
              </Text>
              <Group>
                <Button color="red" loading={merge.isPending} onClick={() => merge.mutate()}>
                  Merge
                </Button>
                <Button variant="default" onClick={() => setConfirm(false)}>
                  Back
                </Button>
              </Group>
            </Stack>
          </Alert>
        ) : (
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              Cancel
            </Button>
            <Button color="red" disabled={!from} onClick={() => setConfirm(true)}>
              Review merge
            </Button>
          </Group>
        )}
      </Stack>
    </Modal>
  )
}
