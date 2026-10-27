import { Menu } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import {
  IconArrowDownRight,
  IconArrowsMaximize,
  IconArrowUpLeft,
  IconCopy,
  IconCrosshair,
  IconExternalLink,
  IconFocus2,
  IconHelpHexagon,
  IconInfoCircle,
  IconMap2,
  IconPhoto,
  IconPinnedOff,
  IconPlus,
  IconRoute,
  IconTable,
  IconX,
} from '@tabler/icons-react'
import { useNavigate } from '@tanstack/react-router'
import { useEffect } from 'react'
import type { InspectorActions } from './Inspector'
import type { MapEdge, MapNode } from './types'

/** What was right-clicked, and where (viewport coordinates of the pointer, or of the node for the keyboard). */
export type MenuTarget =
  | { kind: 'node'; node: MapNode; x: number; y: number }
  | { kind: 'edge'; edge: MapEdge; from?: MapNode; to?: MapNode; x: number; y: number }
  | { kind: 'pane'; x: number; y: number }

export interface ContextMenuActions extends InspectorActions {
  selectEdge: (id: string) => void
  unpin: (id: string) => void
  isPinned: (id: string) => boolean
  fitView: () => void
  copyImage: () => void
  overview: () => void
  clearSelection: () => void
  hasSelection: boolean
  inOverview: boolean
}

const name = (n: MapNode) => (n.redacted ? 'restricted' : (n.displayName ?? n.name))

function copy(text: string, what: string) {
  void navigator.clipboard.writeText(text).then(
    () => notifications.show({ message: `${what} copied`, autoClose: 1500 }),
    () => notifications.show({ color: 'red', message: `Could not copy the ${what.toLowerCase()}` }),
  )
}

/**
 * Right-click (or Shift+F10 / the context-menu key on the selected node) quick actions on the map: details, focus,
 * expand, impact, paths and copying for an entity; the endpoints for a relationship; fit and overview on the canvas.
 * A Mantine menu anchored at the pointer, so arrow keys, Enter and Escape work as in every other menu.
 */
export function MapContextMenu({
  target,
  onClose,
  actions,
}: {
  target: MenuTarget | null
  onClose: () => void
  actions: ContextMenuActions
}) {
  const navigate = useNavigate()
  const open = target !== null
  // Escape closes the menu wherever focus is (a right-click leaves it on the page, not in the menu).
  useEffect(() => {
    if (!open) return
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])
  return (
    <Menu
      opened={target !== null}
      onChange={(open) => {
        if (!open) onClose()
      }}
      position="bottom-start"
      offset={2}
      shadow="md"
      width={270}
      withinPortal
      trapFocus
      // The focus trap focuses the first action instead of an empty placeholder, which an ARIA menu may not contain.
      withInitialFocusPlaceholder={false}
      // Mantine also closes a menu on keydown outside it, which would close a menu opened with Shift+F10 at once;
      // focus is trapped in the open menu, so keys reach it anyway. pointerdown, not mousedown: the map's pan/zoom and
      // node drag handlers (d3) stop mousedown from propagating, so a click on the canvas never reached the document.
      clickOutsideEvents={['pointerdown']}
      returnFocus
    >
      <Menu.Target>
        <div
          aria-hidden
          style={{
            position: 'fixed',
            left: target?.x ?? 0,
            top: target?.y ?? 0,
            width: 1,
            height: 1,
            pointerEvents: 'none',
          }}
        />
      </Menu.Target>
      <Menu.Dropdown data-testid="map-context-menu">
        {target?.kind === 'node' && <NodeItems node={target.node} actions={actions} navigate={navigate} />}
        {target?.kind === 'edge' && <EdgeItems target={target} actions={actions} />}
        {target?.kind === 'pane' && (
          <>
            <Menu.Item leftSection={<IconArrowsMaximize size={14} />} onClick={actions.fitView}>
              Fit view
            </Menu.Item>
            <Menu.Item leftSection={<IconPhoto size={14} />} onClick={actions.copyImage}>
              Copy map as image (1:1)
            </Menu.Item>
            {!actions.inOverview && (
              <Menu.Item leftSection={<IconMap2 size={14} />} onClick={actions.overview}>
                Back to overview
              </Menu.Item>
            )}
            {actions.hasSelection && (
              <Menu.Item leftSection={<IconX size={14} />} onClick={actions.clearSelection}>
                Clear selection
              </Menu.Item>
            )}
          </>
        )}
      </Menu.Dropdown>
    </Menu>
  )
}

function NodeItems({
  node,
  actions,
  navigate,
}: {
  node: MapNode
  actions: ContextMenuActions
  navigate: ReturnType<typeof useNavigate>
}) {
  if (node.cluster) {
    return (
      <>
        <Menu.Item disabled className="deed-menu-title">
          {node.cluster.count} × {node.cluster.value}
        </Menu.Item>
        <Menu.Item leftSection={<IconPlus size={14} />} onClick={() => actions.expand(node)}>
          {node.kind === 'group' ? 'Expand group' : 'Expand cluster'}
        </Menu.Item>
      </>
    )
  }
  const id = node.id
  return (
    <>
      <Menu.Item disabled className="deed-menu-title">
        {name(node)}
      </Menu.Item>
      {!node.redacted && (
        <>
          <Menu.Item
            leftSection={<IconExternalLink size={14} />}
            onClick={() => void navigate({ to: '/entities/$id', params: { id } })}
          >
            Open details
          </Menu.Item>
          <Menu.Item
            leftSection={<IconHelpHexagon size={14} />}
            onClick={() => void navigate({ to: '/entities/$id', params: { id }, search: { tab: 'provenance' } })}
          >
            Why does it look like this?
          </Menu.Item>
        </>
      )}
      <Menu.Item leftSection={<IconInfoCircle size={14} />} onClick={() => actions.select(id)}>
        Inspect here
      </Menu.Item>
      <Menu.Divider role="separator" />
      <Menu.Item leftSection={<IconFocus2 size={14} />} onClick={() => actions.focus(id)}>
        Focus map on this
      </Menu.Item>
      <Menu.Item leftSection={<IconPlus size={14} />} onClick={() => actions.expand(node)}>
        Expand neighbours
      </Menu.Item>
      <Menu.Item leftSection={<IconArrowDownRight size={14} />} onClick={() => actions.impact(id, 'downstream')}>
        What depends on this (downstream)
      </Menu.Item>
      <Menu.Item leftSection={<IconArrowUpLeft size={14} />} onClick={() => actions.impact(id, 'upstream')}>
        What this depends on (upstream)
      </Menu.Item>
      <Menu.Item leftSection={<IconRoute size={14} />} onClick={() => actions.pathFrom(id)}>
        Path from here…
      </Menu.Item>
      <Menu.Item leftSection={<IconRoute size={14} />} onClick={() => actions.pathTo(id)}>
        Path to here…
      </Menu.Item>
      {actions.isPinned(id) && (
        <Menu.Item leftSection={<IconPinnedOff size={14} />} onClick={() => actions.unpin(id)}>
          Reset dragged position
        </Menu.Item>
      )}
      <Menu.Divider role="separator" />
      {!node.redacted && (
        <>
          <Menu.Item
            leftSection={<IconTable size={14} />}
            onClick={() => void navigate({ to: '/relations', search: { entity: id } })}
          >
            Relationships table
          </Menu.Item>
          <Menu.Item leftSection={<IconCopy size={14} />} onClick={() => copy(node.name, 'Name')}>
            Copy name
          </Menu.Item>
        </>
      )}
      <Menu.Item leftSection={<IconCopy size={14} />} onClick={() => copy(id, 'ID')}>
        Copy ID
      </Menu.Item>
    </>
  )
}

function EdgeItems({
  target,
  actions,
}: {
  target: Extract<MenuTarget, { kind: 'edge' }>
  actions: ContextMenuActions
}) {
  const { edge, from, to } = target
  return (
    <>
      <Menu.Item disabled className="deed-menu-title">
        {from ? name(from) : edge.from} → {to ? name(to) : edge.to}
      </Menu.Item>
      <Menu.Item leftSection={<IconInfoCircle size={14} />} onClick={() => actions.selectEdge(edge.id)}>
        Inspect relationship
      </Menu.Item>
      {from && !from.cluster && (
        <Menu.Item leftSection={<IconCrosshair size={14} />} onClick={() => actions.select(from.id)}>
          Go to {name(from)}
        </Menu.Item>
      )}
      {to && !to.cluster && (
        <Menu.Item leftSection={<IconCrosshair size={14} />} onClick={() => actions.select(to.id)}>
          Go to {name(to)}
        </Menu.Item>
      )}
      {edge.id.startsWith('rel_') && (
        <>
          <Menu.Divider role="separator" />
          <Menu.Item leftSection={<IconCopy size={14} />} onClick={() => copy(edge.id, 'Relationship ID')}>
            Copy relationship ID
          </Menu.Item>
        </>
      )}
    </>
  )
}
