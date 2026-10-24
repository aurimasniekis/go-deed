import { Group, Select, Text, type SelectProps } from '@mantine/core'
import { useDebouncedValue } from '@mantine/hooks'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, unwrap } from '@/api/client'
import { keys } from '@/api/queries'

/** Async entity search (GET /entities?q=) as a select; value is the entity ID. */
export function EntityPicker({
  value,
  onChange,
  label,
  placeholder = 'Search entities…',
  initialLabel,
  ...rest
}: {
  value: string | null
  onChange: (id: string | null, label: string) => void
  label?: string
  placeholder?: string
  initialLabel?: string
} & Omit<SelectProps, 'value' | 'onChange' | 'data'>) {
  const [search, setSearch] = useState('')
  const [debounced] = useDebouncedValue(search, 200)
  const q = useQuery({
    queryKey: keys.entities({ picker: true, q: debounced }),
    queryFn: async () =>
      unwrap(await api.GET('/entities', { params: { query: { limit: 15, ...(debounced ? { q: debounced } : {}) } } })),
    placeholderData: keepPreviousData,
    staleTime: 30_000,
  })
  const options = (q.data?.items ?? []).map((e) => ({
    value: e.metadata.id,
    label: `${e.metadata.displayName || e.metadata.name} (${e.kind}, ${e.metadata.project})`,
  }))
  if (value && initialLabel && !options.some((o) => o.value === value)) options.unshift({ value, label: initialLabel })
  return (
    <Select
      label={label}
      placeholder={placeholder}
      searchable
      clearable
      searchValue={search}
      onSearchChange={setSearch}
      data={options}
      value={value}
      onChange={(v) => onChange(v, options.find((o) => o.value === v)?.label ?? '')}
      filter={({ options: o }) => o}
      nothingFoundMessage={q.isFetching ? 'Searching…' : 'No entities match'}
      renderOption={({ option }) => (
        <Group gap={6} wrap="nowrap">
          <Text size="sm" truncate>
            {option.label}
          </Text>
        </Group>
      )}
      {...rest}
    />
  )
}
