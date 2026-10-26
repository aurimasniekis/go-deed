import { MultiSelect } from '@mantine/core'
import { IconFolders } from '@tabler/icons-react'
import { useProjects } from '@/api/queries'
import { setProjectFilter, useMasked, useProjectFilter } from '@/lib/appStore'
import { maskProject } from '@/lib/mask'

/**
 * The global project filter: scopes the dashboard, inventory, map overview, findings and sources. Remembered per user in
 * this browser (a UI preference). Empty = every project you can read.
 */
export function ProjectSwitcher() {
  const projects = useProjects()
  const selected = useProjectFilter()
  const masked = useMasked()
  const data = (projects.data ?? []).map((p) => ({
    value: p.id,
    label: `${masked ? maskProject(p.id) : p.display_name || p.name}${p.synthetic ? ' (synthetic)' : ''}`,
  }))
  const known = new Set(data.map((d) => d.value))
  return (
    <MultiSelect
      aria-label="Project filter"
      data-testid="project-switcher"
      placeholder={selected.length ? undefined : 'All projects'}
      leftSection={<IconFolders size={14} aria-hidden />}
      data={data}
      value={projects.isSuccess ? selected.filter((id) => known.has(id)) : selected}
      onChange={setProjectFilter}
      clearable
      searchable
      w={260}
      maxValues={8}
      comboboxProps={{ withinPortal: true }}
      styles={{ pill: { maxWidth: 120 } }}
    />
  )
}
