import { Badge, Tooltip, UnstyledButton } from '@mantine/core'
import { IconPlugConnectedX, IconRefresh, IconWifi } from '@tabler/icons-react'
import { reconnectLive, useLiveStatus } from '@/api/useLive'
import type { LiveStatus } from '@/api/live'

const meta: Record<LiveStatus, { label: string; color: string; tip: string }> = {
  live: { label: 'Live', color: 'green', tip: 'Receiving live changes from the server.' },
  connecting: { label: 'Connecting', color: 'gray', tip: 'Opening the live update stream…' },
  reconnecting: {
    label: 'Reconnecting',
    color: 'yellow',
    tip: 'The live stream dropped; the browser is reconnecting and will replay what was missed.',
  },
  offline: {
    label: 'Disconnected',
    color: 'red',
    tip: 'Live updates are paused. Retrying automatically; data on screen may be out of date. Click to retry now.',
  },
  closed: {
    label: 'Disconnected',
    color: 'red',
    tip: 'The server closed the live stream (credentials expired or revoked). Click to reconnect.',
  },
}

/** Small live/disconnected indicator for the SSE connection (icon + text, not colour alone). */
export function LiveIndicator() {
  const status = useLiveStatus()
  const m = meta[status]
  const down = status === 'offline' || status === 'closed'
  const icon = down ? (
    <IconPlugConnectedX size={12} />
  ) : status === 'live' ? (
    <IconWifi size={12} />
  ) : (
    <IconRefresh size={12} />
  )
  return (
    <Tooltip label={m.tip} multiline maw={260}>
      <UnstyledButton
        onClick={down ? reconnectLive : undefined}
        aria-label={`Live updates: ${m.label}${down ? '. Retry now' : ''}`}
        data-testid="live-indicator"
        data-status={status}
      >
        <Badge color={m.color} variant="light" leftSection={icon} styles={{ label: { textTransform: 'none' } }}>
          {m.label}
        </Badge>
      </UnstyledButton>
    </Tooltip>
  )
}
