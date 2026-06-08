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

// Russian time zones for CSV import (Feibot flash dumps are zoneless local
// time). Values are IANA names — the backend validates them via time.LoadLocation.
export const RU_TIMEZONES = [
  {value: 'Europe/Kaliningrad', label: 'Калининград (UTC+2)'},
  {value: 'Europe/Moscow', label: 'Москва (UTC+3)'},
  {value: 'Europe/Samara', label: 'Самара (UTC+4)'},
  {value: 'Asia/Yekaterinburg', label: 'Екатеринбург (UTC+5)'},
  {value: 'Asia/Omsk', label: 'Омск (UTC+6)'},
  {value: 'Asia/Krasnoyarsk', label: 'Красноярск (UTC+7)'},
  {value: 'Asia/Irkutsk', label: 'Иркутск (UTC+8)'},
  {value: 'Asia/Yakutsk', label: 'Якутск (UTC+9)'},
  {value: 'Asia/Vladivostok', label: 'Владивосток (UTC+10)'},
  {value: 'Asia/Magadan', label: 'Магадан (UTC+11)'},
  {value: 'Asia/Kamchatka', label: 'Камчатка (UTC+12)'},
]

// cleanToMs parses an elapsed clean time "[HH:]MM:SS[.mmm]" into milliseconds.
// Returns null when the string is empty or malformed (run5's ManualTimeEntry).
export function cleanToMs(str) {
  if (!str) return null
  const m = String(str).trim().match(/^(?:(\d+):)?(\d{1,2}):(\d{1,2})(?:\.(\d{1,3}))?$/)
  if (!m) return null
  const h = m[1] ? Number(m[1]) : 0
  const min = Number(m[2])
  const sec = Number(m[3])
  const millis = m[4] ? Number(m[4].padEnd(3, '0')) : 0
  if (min > 59 || sec > 59) return null
  return ((h * 3600 + min * 60 + sec) * 1000) + millis
}
