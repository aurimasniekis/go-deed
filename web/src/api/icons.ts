import { queryOptions, useQuery } from '@tanstack/react-query'
import { useEffect } from 'react'
import { customIcons } from '@/components/icons'
import { api, unwrap, type Schemas } from './client'

export type CustomIcon = Schemas['Icon']

export const customIconsQuery = queryOptions({
  queryKey: ['icons'] as const,
  queryFn: async () => {
    const res = await api.GET('/icons')
    // Older servers have no icon store; treat that as "no custom icons".
    if (res.response.status === 404) return [] as CustomIcon[]
    return unwrap(res)
  },
  staleTime: 5 * 60_000,
})

/** Loads custom icons once per session so RefIcon can resolve `custom:<name>` (URL with a cache-busting hash, tint). */
export function useCustomIconsLoader(enabled: boolean) {
  const q = useQuery({ ...customIconsQuery, enabled })
  useEffect(() => {
    if (!q.data) return
    customIcons.setState(() =>
      Object.fromEntries(
        q.data.map((i) => [i.name, { url: `${i.url}?v=${i.sha256.slice(0, 12)}`, monochrome: i.monochrome }]),
      ),
    )
  }, [q.data])
}
