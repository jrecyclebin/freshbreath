// The reader's own server and app, filled into every "say this" prompt.
// Kept in localStorage so it follows them from page to page. SSR renders the
// placeholders; the saved values load once a component mounts.
import { reactive, watch } from 'vue'

const KEY = 'frbr-docs:setup'

export const setup = reactive({ server: '', app: '', nonce: '', slug: '' })

const fallback: Record<string, string> = {
  server: 'http://localhost:9009',
  app: '[app name]',
  nonce: 'YOUR-NONCE',
  slug: 'your-app'
}

let loaded = false
export function loadSetup() {
  if (loaded || typeof window === 'undefined') return
  loaded = true
  try {
    Object.assign(setup, JSON.parse(localStorage.getItem(KEY) || '{}'))
  } catch {}
  watch(setup, (v) => {
    try { localStorage.setItem(KEY, JSON.stringify(v)) } catch {}
  })
}

// The value to show for a placeholder, and whether the reader filled it in.
export function setupValue(key: string): { text: string; filled: boolean } {
  let v = ((setup as any)[key] || '').trim()
  if (key === 'server') v = v.replace(/\/+$/, '')
  if (key === 'slug') v = v.replace(/^\/+|\/+$/g, '')
  return v ? { text: v, filled: true } : { text: fallback[key], filled: false }
}
