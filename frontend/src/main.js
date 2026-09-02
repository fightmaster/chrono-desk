import './style.css'
import {mount} from 'svelte'
import App from './App.svelte'

const target = document.getElementById('app')

if (!target) {
  throw new Error('Chrono Desk mount target is missing')
}

const app = mount(App, {target})
document.getElementById('boot-fallback')?.remove()

export default app
