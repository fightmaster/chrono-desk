import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [svelte()],
  build: {
    // Big Sur 11 shipped with Safari/WebKit 14. Keep the embedded Wails UI
    // compatible with the competition MacBook rather than Vite's moving
    // baseline-browser default.
    target: 'safari14'
  }
})
