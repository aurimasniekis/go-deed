import { Alert, Button, Group, List, SimpleGrid, Stack, Text, Textarea } from '@mantine/core'
import { IconChecklist } from '@tabler/icons-react'
import { useMutation } from '@tanstack/react-query'
import { api, errorMessage, unwrap, type Schemas } from '@/api/client'

export const SCHEMA_TEMPLATE = JSON.stringify(
  {
    $schema: 'https://json-schema.org/draft/2020-12/schema',
    type: 'object',
    properties: {
      endpoint: { type: 'string', format: 'uri' },
      replicas: { type: 'integer', minimum: 0 },
    },
    required: ['endpoint'],
    additionalProperties: false,
  },
  null,
  2,
)

export function parseJson(text: string): { value?: unknown; error?: string } {
  if (!text.trim()) return { value: undefined }
  try {
    return { value: JSON.parse(text) as unknown }
  } catch (e) {
    return { error: (e as Error).message }
  }
}

/**
 * JSON Schema editor with a server-side dry run (POST /kinds/{name}/schema-check): schema validity (2020-12, bounded),
 * compatibility problems against the kind's current storage version, and validation of a sample spec.
 */
export function SchemaEditor({
  kindName,
  schema,
  onSchema,
  sample,
  onSample,
}: {
  kindName: string
  schema: string
  onSchema: (s: string) => void
  sample: string
  onSample: (s: string) => void
}) {
  const parsed = parseJson(schema)
  const parsedSample = parseJson(sample)
  const check = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/kinds/{name}/schema-check', {
          params: { path: { name: kindName || 'new_kind' } },
          body: {
            schema: parsed.value ?? {},
            ...(parsedSample.value && typeof parsedSample.value === 'object'
              ? { spec: parsedSample.value as Record<string, unknown> }
              : {}),
          },
        }),
      ),
  })
  const res: Schemas['SchemaCheck'] | undefined = check.data
  return (
    <Stack gap="sm">
      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Textarea
          label="JSON Schema (2020-12) for spec"
          description="No remote $ref; at most 64 KiB, depth 16, 512 properties. Patterns use RE2."
          value={schema}
          onChange={(e) => onSchema(e.currentTarget.value)}
          autosize
          minRows={14}
          maxRows={30}
          spellCheck={false}
          styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
          error={parsed.error}
          data-testid="schema-editor"
        />
        <Textarea
          label="Sample spec (optional)"
          description="Validated against the schema in the dry run."
          value={sample}
          onChange={(e) => onSample(e.currentTarget.value)}
          autosize
          minRows={14}
          maxRows={30}
          spellCheck={false}
          styles={{ input: { fontFamily: 'var(--deed-mono)', fontSize: 12 } }}
          error={parsedSample.error}
        />
      </SimpleGrid>
      <Group>
        <Button
          variant="default"
          leftSection={<IconChecklist size={14} />}
          onClick={() => check.mutate()}
          loading={check.isPending}
          disabled={!!parsed.error || !!parsedSample.error}
          data-testid="schema-check"
        >
          Check schema (dry run)
        </Button>
      </Group>
      {check.isError && <Alert color="red">{errorMessage(check.error)}</Alert>}
      {res && (
        <Alert
          color={
            !res.valid ? 'red' : res.compatibility_problems.length || res.sample_errors.length ? 'yellow' : 'green'
          }
          title={
            !res.valid
              ? 'The schema is invalid'
              : res.compatibility_problems.length
                ? 'Valid, but not backward compatible'
                : 'The schema is valid'
          }
          data-testid="schema-check-result"
        >
          {res.error && <Text size="sm">{res.error}</Text>}
          {res.compatibility_problems.length > 0 && (
            <>
              <Text size="sm" fw={500}>
                Compatibility with the current storage version (publishing needs “breaking” and a migration):
              </Text>
              <List size="sm">
                {res.compatibility_problems.map((p) => (
                  <List.Item key={p}>{p}</List.Item>
                ))}
              </List>
            </>
          )}
          {parsedSample.value !== undefined &&
            (res.sample_errors.length ? (
              <>
                <Text size="sm" fw={500}>
                  The sample fails validation:
                </Text>
                <List size="sm">
                  {res.sample_errors.map((f) => (
                    <List.Item key={f.field}>
                      <span className="mono">{f.field}</span>: {f.message}
                    </List.Item>
                  ))}
                </List>
              </>
            ) : (
              res.valid && <Text size="sm">The sample spec validates.</Text>
            ))}
        </Alert>
      )}
    </Stack>
  )
}
