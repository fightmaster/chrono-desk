// Thin client for the embedded HTTP API.
let base = ''

export function setBase(url) {
  base = url
}

export async function call(method, path, body, contentType) {
  const opts = {method}
  if (body !== undefined) {
    opts.body = body
    opts.headers = {'Content-Type': contentType || 'application/json'}
  }
  const resp = await fetch(`${base}${path}`, opts)
  const data = await resp.json()
  if (!resp.ok) throw new Error(data.error || resp.status)
  return data
}

// Local-time <input type="datetime-local"> helpers (unix ms ↔ input value).
export function msToInput(ms) {
  if (ms === null || ms === undefined) return ''
  const d = new Date(ms)
  const pad = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

export function inputToMs(value) {
  if (!value) return null
  const ms = new Date(value).getTime()
  return Number.isNaN(ms) ? null : ms
}

export function fmtTime(ms) {
  if (ms === null || ms === undefined) return ''
  const d = new Date(ms)
  const pad = n => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(ms % 1000).padStart(3, '0')}`
}
