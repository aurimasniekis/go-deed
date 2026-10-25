import { Autocomplete, Group, Text, type ComboboxItem, type OptionsFilter } from '@mantine/core'
import { useSelector } from '@tanstack/react-store'
import { useMemo } from 'react'
import { customIcons, iconChoices, loadTheSvgIcons, RefIcon, theSvgIcons } from './icons'

/**
 * A `pack:name` icon reference input that suggests every bundled icon (Tabler, Simple Icons, theSVG) and the uploaded
 * custom icons, searchable by reference or title. Free text stays allowed: the server validates the format.
 */
export function IconPicker({
  label,
  description,
  value,
  onChange,
}: {
  label: string
  description?: string
  value: string
  onChange: (value: string) => void
}) {
  const custom = useSelector(customIcons, (s) => s)
  const theSvg = useSelector(theSvgIcons, (s) => s)
  const choices = useMemo(() => iconChoices(Object.keys(custom), theSvg), [custom, theSvg])
  const titles = useMemo(() => new Map(choices.map((c) => [c.ref, c.title.toLowerCase()])), [choices])
  const filter: OptionsFilter = ({ options, search, limit }) => {
    const q = search.trim().toLowerCase()
    const items = options as ComboboxItem[]
    const hits = q ? items.filter((o) => o.value.includes(q) || titles.get(o.value)?.includes(q)) : items
    return hits.slice(0, limit)
  }
  return (
    <Autocomplete
      label={label}
      description={description}
      value={value}
      onChange={onChange}
      onDropdownOpen={() => void loadTheSvgIcons()}
      data={choices.map((c) => c.ref)}
      filter={filter}
      limit={50}
      maxDropdownHeight={280}
      leftSection={<RefIcon iconRef={value || 'tabler:cube'} size={14} />}
      renderOption={({ option }) => (
        <Group gap="xs" wrap="nowrap">
          <RefIcon iconRef={option.value} size={16} />
          <Text size="xs" ff="var(--deed-mono)">
            {option.value}
          </Text>
        </Group>
      )}
      data-testid="icon-picker"
    />
  )
}
