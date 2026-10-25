import { Alert, Button, Center, Group, Skeleton, Stack, Text, ThemeIcon, Title } from '@mantine/core'
import {
  IconAlertTriangle,
  IconClockExclamation,
  IconLock,
  IconMoodEmpty,
  IconRefresh,
  IconSearchOff,
} from '@tabler/icons-react'
import type { ReactNode } from 'react'
import { ApiError, errorMessage } from '@/api/client'
import { relativeTime } from '@/lib/format'

/** Skeleton rows for tables and lists while the first page loads. */
export function LoadingRows({
  rows = 6,
  height = 28,
  label = 'Loading',
}: {
  rows?: number
  height?: number
  label?: string
}) {
  return (
    <Stack gap={6} aria-busy="true" aria-label={label} role="status">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} height={height} radius="sm" />
      ))}
    </Stack>
  )
}

/** Skeleton card grid for dashboards. */
export function LoadingBlock({ height = 160, label = 'Loading' }: { height?: number; label?: string }) {
  return <Skeleton height={height} radius="sm" aria-busy="true" aria-label={label} role="status" />
}

export function EmptyState({
  title,
  children,
  action,
  icon,
}: {
  title: string
  children?: ReactNode
  action?: ReactNode
  icon?: ReactNode
}) {
  return (
    <Center py="xl" px="md">
      <Stack align="center" gap="xs" maw={460} ta="center">
        <ThemeIcon variant="light" color="gray" size={40} radius="xl" aria-hidden>
          {icon ?? <IconMoodEmpty size={22} stroke={1.6} />}
        </ThemeIcon>
        <Title order={3}>{title}</Title>
        {children && (
          <Text size="sm" c="dimmed">
            {children}
          </Text>
        )}
        {action}
      </Stack>
    </Center>
  )
}

/**
 * Errors with the right framing: 403 (permission denied), 404 (not found or not visible to you — the API hides
 * unreadable projects as not found), and everything else with a retry.
 */
export function ErrorState({
  error,
  onRetry,
  what = 'this page',
}: {
  error: unknown
  onRetry?: () => void
  what?: string
}) {
  if (error instanceof ApiError && error.status === 403) {
    return (
      <EmptyState title="Permission denied" icon={<IconLock size={22} stroke={1.6} />}>
        Your role does not allow access to {what}. Ask a project or installation admin if you need it. ({error.message})
      </EmptyState>
    )
  }
  if (error instanceof ApiError && error.status === 404) {
    return (
      <EmptyState title="Not found" icon={<IconSearchOff size={22} stroke={1.6} />}>
        It does not exist, was removed, or belongs to a project you cannot read.
      </EmptyState>
    )
  }
  return (
    <Alert color="red" title={`Could not load ${what}`} icon={<IconAlertTriangle size={18} />} role="alert">
      <Stack gap="xs">
        <Text size="sm">{errorMessage(error)}</Text>
        {onRetry && (
          <Group>
            <Button size="xs" variant="default" leftSection={<IconRefresh size={14} />} onClick={onRetry}>
              Retry
            </Button>
          </Group>
        )}
      </Stack>
    </Alert>
  )
}

/** Inline notice when a section failed while the rest of the page loaded (partial failure). */
export function PartialFailure({ what, error, onRetry }: { what: string; error: unknown; onRetry?: () => void }) {
  return (
    <Alert
      color="orange"
      variant="light"
      icon={<IconAlertTriangle size={16} />}
      title={`${what} unavailable`}
      role="status"
    >
      <Group gap="xs" justify="space-between" wrap="nowrap">
        <Text size="sm">{errorMessage(error)}</Text>
        {onRetry && (
          <Button size="compact-xs" variant="default" onClick={onRetry}>
            Retry
          </Button>
        )}
      </Group>
    </Alert>
  )
}

/** Shown when the data on screen could not be refreshed (the last good copy stays visible). */
export function StaleNotice({ updatedAt, onRetry }: { updatedAt: number; onRetry?: () => void }) {
  return (
    <Alert color="yellow" variant="light" icon={<IconClockExclamation size={16} />} role="status" py={6}>
      <Group gap="xs" justify="space-between" wrap="nowrap">
        <Text size="sm">Showing data from {relativeTime(updatedAt)}; the latest refresh failed.</Text>
        {onRetry && (
          <Button size="compact-xs" variant="default" onClick={onRetry}>
            Retry
          </Button>
        )}
      </Group>
    </Alert>
  )
}

/** Wraps a TanStack Query result: skeleton, error (403/404 aware), stale notice, or children. */
export function QueryState<T>({
  query,
  what,
  loading,
  children,
}: {
  query: {
    data: T | undefined
    error: unknown
    isPending: boolean
    isError: boolean
    dataUpdatedAt: number
    refetch: () => unknown
  }
  what?: string
  loading?: ReactNode
  children: (data: T) => ReactNode
}) {
  if (query.isPending) return <>{loading ?? <LoadingRows />}</>
  if (query.data === undefined) {
    return <ErrorState error={query.error} what={what} onRetry={() => void query.refetch()} />
  }
  return (
    <>
      {query.isError && <StaleNotice updatedAt={query.dataUpdatedAt} onRetry={() => void query.refetch()} />}
      {children(query.data)}
    </>
  )
}
