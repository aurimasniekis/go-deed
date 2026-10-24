import { Alert, Button, Group, List, Modal, SegmentedControl, Stack, TagsInput, Text, TextInput } from '@mantine/core'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, errorMessage, unwrap, type Entity, type Schemas } from '@/api/client'

type Action = 'set_labels' | 'remove_labels' | 'owner' | 'environment'

const actionLabels: Record<Action, string> = {
  set_labels: 'Set labels',
  remove_labels: 'Remove labels',
  owner: 'Set owner',
  environment: 'Set environment',
}

/** Parses "key=value" tags; returns null when any tag is malformed. */
export function parseLabelTags(tags: string[]): Record<string, string> | null {
  const out: Record<string, string> = {}
  for (const t of tags) {
    const i = t.indexOf('=')
    if (i <= 0) return null
    out[t.slice(0, i).trim()] = t.slice(i + 1).trim()
  }
  return out
}

/**
 * Bulk annotation (POST /entities/bulk): writes each entity's UI annotation facet — labels, owner, environment. These
 * rank below declarations (ADR-003), so a declared value still wins; the confirmation says so. Per-row errors are
 * reported by entity.
 */
export function BulkActionsModal({
  opened,
  entities,
  onClose,
  onDone,
}: {
  opened: boolean
  entities: Entity[]
  onClose: () => void
  onDone: () => void
}) {
  const qc = useQueryClient()
  const [action, setAction] = useState<Action>('set_labels')
  const [tags, setTags] = useState<string[]>([])
  const [value, setValue] = useState('')
  const [confirming, setConfirming] = useState(false)
  const [result, setResult] = useState<Schemas['BulkResult'] | null>(null)

  const labels = action === 'set_labels' ? parseLabelTags(tags) : null
  const invalid =
    (action === 'set_labels' && (tags.length === 0 || labels === null)) ||
    (action === 'remove_labels' && tags.length === 0) ||
    (action === 'environment' && value !== '' && !/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(value))

  const run = useMutation({
    mutationFn: async () => {
      const body: Schemas['BulkInputBody'] = { ids: entities.map((e) => e.metadata.id) }
      if (action === 'set_labels' && labels) body.set_labels = labels
      if (action === 'remove_labels') body.remove_labels = tags
      if (action === 'owner') body.owner = value.trim()
      if (action === 'environment') body.environment = value.trim()
      return unwrap(await api.POST('/entities/bulk', { body }))
    },
    onSuccess: (r) => {
      setResult(r)
      setConfirming(false)
      void qc.invalidateQueries({ queryKey: ['entities'] })
      void qc.invalidateQueries({ queryKey: ['dashboard'] })
      if (r.errors.length === 0) onDone()
    },
  })

  const nameOf = new Map(entities.map((e) => [e.metadata.id, `${e.kind}/${e.metadata.name}`]))
  const close = () => {
    setResult(null)
    setConfirming(false)
    run.reset()
    onClose()
  }

  return (
    <Modal
      opened={opened}
      onClose={close}
      title={`Bulk edit ${entities.length} ${entities.length === 1 ? 'entity' : 'entities'}`}
      size="lg"
      centered
    >
      {result ? (
        <Stack gap="sm">
          <Alert
            color={result.errors.length ? 'orange' : 'green'}
            title={`${result.updated} updated, ${result.errors.length} failed`}
          >
            {result.errors.length > 0 && (
              <List size="sm" spacing={2}>
                {result.errors.map((e) => (
                  <List.Item key={e.field}>
                    <b>{nameOf.get(e.field) ?? e.field}</b>: {e.message}
                  </List.Item>
                ))}
              </List>
            )}
          </Alert>
          <Group justify="flex-end">
            <Button onClick={close}>Close</Button>
          </Group>
        </Stack>
      ) : (
        <Stack gap="sm">
          <SegmentedControl
            value={action}
            onChange={(v) => {
              setAction(v as Action)
              setTags([])
              setValue('')
              setConfirming(false)
            }}
            data={Object.entries(actionLabels).map(([value, label]) => ({ value, label }))}
            fullWidth
          />
          {(action === 'set_labels' || action === 'remove_labels') && (
            <TagsInput
              label={action === 'set_labels' ? 'Labels (key=value)' : 'Label keys to remove'}
              placeholder={action === 'set_labels' ? 'team=platform' : 'team'}
              value={tags}
              onChange={setTags}
              error={action === 'set_labels' && labels === null ? 'Each label must be key=value' : undefined}
              splitChars={[',', ' ']}
              data-autofocus
            />
          )}
          {(action === 'owner' || action === 'environment') && (
            <TextInput
              label={action === 'owner' ? 'Owner (group name or ID)' : 'Environment'}
              description="Leave empty to clear the annotation."
              value={value}
              onChange={(e) => setValue(e.currentTarget.value)}
              error={invalid ? 'Lowercase letters, digits and dashes' : undefined}
              data-autofocus
            />
          )}
          <Alert color="blue" variant="light">
            Bulk edits write each entity’s <b>UI annotation</b>. Annotations rank below declarations (Terraform, API),
            so a declared value keeps winning; they override imported and discovered values.
          </Alert>
          {run.isError && (
            <Alert color="red" role="alert">
              {errorMessage(run.error)}
            </Alert>
          )}
          {confirming ? (
            <Group justify="space-between">
              <Text size="sm">
                Apply “{actionLabels[action]}” to {entities.length} {entities.length === 1 ? 'entity' : 'entities'}?
              </Text>
              <Group gap="xs">
                <Button variant="default" onClick={() => setConfirming(false)} disabled={run.isPending}>
                  Back
                </Button>
                <Button onClick={() => run.mutate()} loading={run.isPending} data-testid="bulk-confirm">
                  Confirm
                </Button>
              </Group>
            </Group>
          ) : (
            <Group justify="flex-end">
              <Button variant="default" onClick={close}>
                Cancel
              </Button>
              <Button
                onClick={() => setConfirming(true)}
                disabled={invalid || entities.length === 0}
                data-testid="bulk-apply"
              >
                Review
              </Button>
            </Group>
          )}
        </Stack>
      )}
    </Modal>
  )
}
