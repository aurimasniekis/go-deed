import { join } from 'node:path'

export const E2E_PORT = Number(process.env.DEED_E2E_PORT ?? '48787')
export const E2E_BASE_URL = `http://127.0.0.1:${E2E_PORT}`
export const MOCKS_PORT = E2E_PORT + 2
export const STATE_DIR = join(import.meta.dirname, '.state')
export const STORAGE_STATE = join(STATE_DIR, 'storage.json')
export const OWNER_STATE = join(STATE_DIR, 'owner.json')
export const SCREENSHOTS = join(import.meta.dirname, 'screenshots')
