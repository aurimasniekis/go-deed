import { AnchorLink } from '@/components/links'
import {
  ActionIcon,
  Alert,
  Box,
  Button,
  Code,
  CopyButton,
  Group,
  Modal,
  ScrollArea,
  Stack,
  Text,
  TextInput,
  Title,
  Tooltip,
} from '@mantine/core'
import { IconCheck, IconCopy, IconDownload } from '@tabler/icons-react'
import { useMemo, useState, type ReactNode } from 'react'
import { useKinds } from '@/api/queries'
import type { KindView } from '@/api/client'
import { dateTime, isoTitle, relativeTime } from '@/lib/format'
import { RefIcon } from './icons'

export function PageHeader({
  title,
  description,
  actions,
  children,
}: {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  children?: ReactNode
}) {
  return (
    <Stack gap={4} mb="md">
      <Group justify="space-between" align="flex-start" wrap="wrap" gap="sm">
        <Box style={{ minWidth: 0 }}>
          <Title order={1}>{title}</Title>
          {description && (
            <Text size="sm" c="dimmed" mt={2}>
              {description}
            </Text>
          )}
        </Box>
        {actions && <Group gap="xs">{actions}</Group>}
      </Group>
      {children}
    </Stack>
  )
}

/** Relative time with the absolute timestamp in a tooltip and a machine-readable <time>. */
export function TimeAgo({
  value,
  fallback = 'never',
}: {
  value: string | number | undefined | null
  fallback?: string
}) {
  const iso = isoTitle(value)
  if (!iso) return <span>{fallback}</span>
  return (
    <Tooltip label={dateTime(value)}>
      <time dateTime={iso}>{relativeTime(value)}</time>
    </Tooltip>
  )
}

/** Kind name → kind definition (icons, titles, colours). */
export function useKindMap(): Map<string, KindView> {
  const kinds = useKinds()
  return useMemo(() => new Map((kinds.data ?? []).map((k) => [k.name, k])), [kinds.data])
}

export function KindIcon({ kind, size = 16 }: { kind: string; size?: number }) {
  const k = useKindMap().get(kind)
  return <RefIcon iconRef={k?.icon ?? 'tabler:cube'} size={size} />
}

export function KindLabel({ kind, size = 14 }: { kind: string; size?: number }) {
  const k = useKindMap().get(kind)
  return (
    <Group gap={6} wrap="nowrap" component="span">
      <RefIcon iconRef={k?.icon ?? 'tabler:cube'} size={size} />
      <Text span size="sm">
        {k?.title ?? kind}
      </Text>
    </Group>
  )
}

/** Link to an entity's detail page (restricted entities are not links). */
export function EntityLink({
  id,
  name,
  kind,
  redacted,
  withIcon = true,
}: {
  id: string
  name: string
  kind?: string
  redacted?: boolean
  withIcon?: boolean
}) {
  if (redacted) {
    return (
      <Tooltip label="This entity belongs to a project you cannot read.">
        <Text span size="sm" c="dimmed" fs="italic">
          restricted
        </Text>
      </Tooltip>
    )
  }
  return (
    <Group gap={6} wrap="nowrap" component="span" style={{ minWidth: 0 }}>
      {withIcon && kind && <KindIcon kind={kind} size={14} />}
      <AnchorLink to="/entities/$id" params={{ id }} size="sm" truncate="end">
        {name}
      </AnchorLink>
    </Group>
  )
}

export function CopyIcon({ value, label = 'Copy' }: { value: string; label?: string }) {
  return (
    <CopyButton value={value} timeout={1500}>
      {({ copied, copy }) => (
        <Tooltip label={copied ? 'Copied' : label}>
          <ActionIcon size="sm" onClick={copy} aria-label={label} color={copied ? 'teal' : 'gray'}>
            {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
          </ActionIcon>
        </Tooltip>
      )}
    </CopyButton>
  )
}

export function Mono({ children, c }: { children: ReactNode; c?: string }) {
  return (
    <Text span className="mono" c={c}>
      {children}
    </Text>
  )
}

export function JsonBlock({ value, maxHeight = 320 }: { value: unknown; maxHeight?: number }) {
  return (
    <ScrollArea.Autosize mah={maxHeight} type="auto" viewportProps={{ tabIndex: 0, 'aria-label': 'JSON document' }}>
      <Code block style={{ whiteSpace: 'pre', fontSize: 12 }}>
        {JSON.stringify(value, null, 2)}
      </Code>
    </ScrollArea.Autosize>
  )
}

/** A confirmation dialog for destructive or bulk actions. */
export function ConfirmModal({
  opened,
  title,
  children,
  confirmLabel = 'Confirm',
  danger = false,
  pending = false,
  typeToConfirm,
  onConfirm,
  onClose,
}: {
  opened: boolean
  title: string
  children: ReactNode
  confirmLabel?: string
  danger?: boolean
  pending?: boolean
  typeToConfirm?: string
  onConfirm: () => void
  onClose: () => void
}) {
  const [typed, setTyped] = useState('')
  const blocked = typeToConfirm !== undefined && typed !== typeToConfirm
  return (
    <Modal
      opened={opened}
      onClose={() => {
        setTyped('')
        onClose()
      }}
      title={title}
      centered
    >
      <Stack gap="sm">
        <Box fz="sm">{children}</Box>
        {typeToConfirm !== undefined && (
          <TextInput
            label={
              <>
                Type <Code>{typeToConfirm}</Code> to confirm
              </>
            }
            value={typed}
            onChange={(e) => setTyped(e.currentTarget.value)}
            data-autofocus
          />
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button color={danger ? 'red' : undefined} onClick={onConfirm} loading={pending} disabled={blocked}>
            {confirmLabel}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}

/** Shows a secret exactly once (tokens, setup links, recovery codes) with copy and optional download. */
export function OneTimeSecret({
  opened,
  title,
  intro,
  secret,
  lines,
  downloadName,
  onClose,
}: {
  opened: boolean
  title: string
  intro: ReactNode
  secret?: string
  lines?: string[]
  downloadName?: string
  onClose: () => void
}) {
  const text = secret ?? (lines ?? []).join('\n')
  const download = () => {
    const blob = new Blob([`${text}\n`], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = downloadName ?? 'deed-secret.txt'
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <Modal opened={opened} onClose={onClose} title={title} centered size="lg" closeOnClickOutside={false}>
      <Stack gap="sm">
        <Alert color="yellow" variant="light">
          {intro} It will not be shown again.
        </Alert>
        {secret !== undefined && (
          <Group gap="xs" wrap="nowrap">
            <Code
              block
              style={{ flex: 1, wordBreak: 'break-all', whiteSpace: 'pre-wrap', fontSize: 12 }}
              data-testid="one-time-secret"
            >
              {secret}
            </Code>
            <CopyIcon value={secret} />
          </Group>
        )}
        {lines && (
          <Code block data-testid="one-time-lines" style={{ fontSize: 13, columns: 2 }}>
            {lines.join('\n')}
          </Code>
        )}
        <Group justify="space-between">
          <Group gap="xs">
            <CopyButton value={text}>
              {({ copied, copy }) => (
                <Button
                  variant="default"
                  leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                  onClick={copy}
                >
                  {copied ? 'Copied' : 'Copy'}
                </Button>
              )}
            </CopyButton>
            {downloadName && (
              <Button variant="default" leftSection={<IconDownload size={14} />} onClick={download}>
                Download
              </Button>
            )}
          </Group>
          <Button onClick={onClose}>I have stored it</Button>
        </Group>
      </Stack>
    </Modal>
  )
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <Text
      component="kbd"
      size="xs"
      px={5}
      py={1}
      style={{
        border: '1px solid var(--deed-border)',
        borderRadius: 4,
        background: 'var(--deed-surface-2)',
        fontFamily: 'var(--deed-mono)',
      }}
    >
      {children}
    </Text>
  )
}

/** Key/value rows for detail panels. */
export function Field({ label, children, why }: { label: string; children: ReactNode; why?: ReactNode }) {
  return (
    <Group gap="sm" wrap="nowrap" align="flex-start" py={4} style={{ borderBottom: '1px solid var(--deed-border)' }}>
      <Text size="sm" c="dimmed" w={150} style={{ flexShrink: 0 }}>
        {label}
      </Text>
      <Box style={{ flex: 1, minWidth: 0, fontSize: 13 }}>{children}</Box>
      {why}
    </Group>
  )
}
