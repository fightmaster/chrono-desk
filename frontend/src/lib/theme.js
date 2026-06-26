// Theme store: 'day' (default, light) | 'night'. Persisted to localStorage and
// applied as a `data-theme` attribute on the workspace wrapper (see App.svelte).
import {writable} from 'svelte/store'

const KEY = 'chrono-desk-theme'
const initial = (typeof localStorage !== 'undefined' && localStorage.getItem(KEY)) || 'day'

export const theme = writable(initial)

theme.subscribe(v => {
  if (typeof localStorage !== 'undefined') localStorage.setItem(KEY, v)
})

export function toggleTheme() {
  theme.update(v => (v === 'night' ? 'day' : 'night'))
}
