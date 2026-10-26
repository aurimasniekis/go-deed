import { Store, useSelector } from '@tanstack/react-store'
import { useEffect } from 'react'
import { readPref, writePref } from './prefs'

/**
 * App-wide UI state: the global project filter (persisted per user in localStorage — a preference, never a credential)
 * and the shortcuts-help dialog.
 */
export interface AppState {
  user: string
  projects: string[]
  shortcutsOpen: boolean
  /** The dashboard's synthetic_data flag (drives the persistent banner). */
  syntheticData: boolean
  /** Mask mode: names and other identifying text replaced by stable aliases (screenshots, screen sharing). */
  masked: boolean
}

export const appStore = new Store<AppState>({
  user: '',
  projects: [],
  shortcutsOpen: false,
  syntheticData: false,
  masked: readPref<boolean>('masked', false),
})

export function setMasked(masked: boolean) {
  writePref('masked', masked)
  appStore.setState((s) => ({ ...s, masked }))
}

export function useMasked(): boolean {
  return useSelector(appStore, (s) => s.masked)
}

export function setSyntheticData(v: boolean) {
  if (appStore.state.syntheticData !== v) appStore.setState((s) => ({ ...s, syntheticData: v }))
}

export function setProjectFilter(projects: string[]) {
  appStore.setState((s) => ({ ...s, projects }))
  writePref('projects', projects, appStore.state.user)
}

export function useProjectFilter(): string[] {
  return useSelector(appStore, (s) => s.projects)
}

/** Loads the signed-in user's saved project filter. */
export function useLoadUserPrefs(userId: string | undefined) {
  useEffect(() => {
    if (!userId) return
    const projects = readPref<string[]>('projects', [], userId)
    appStore.setState((s) => ({ ...s, user: userId, projects: Array.isArray(projects) ? projects : [] }))
  }, [userId])
}

export function setShortcutsOpen(open: boolean) {
  appStore.setState((s) => ({ ...s, shortcutsOpen: open }))
}

export function useTitle(title: string) {
  useEffect(() => {
    document.title = title ? `${title} · Deed` : 'Deed'
  }, [title])
}
