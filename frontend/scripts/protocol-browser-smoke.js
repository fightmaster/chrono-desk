// Loaded only by runtime-smoke.mjs --protocol, never by the application bundle.
// Exercise the real built Svelte UI against deterministic in-memory API replies.
window.go = {main: {App: {
  APIBaseURL: () => Promise.resolve(location.origin),
  APIToken: () => Promise.resolve('local-smoke-token'),
}}}

const race = {id: 'race', name: 'Тестовая дистанция', format: 'FixedDistance', started_at_ms: 1000}
const categories = [{id: 'm', name: 'Мужчины'}, {id: 'f', name: 'Женщины'}]
const members = [
  {id: 'finished', number: 10, first_name: 'Иван', last_name: 'Победитель', gender: 'male', category_id: 'm', finish_time_ms: 2000},
  {id: 'dns', number: 11, first_name: 'Пётр', last_name: 'НеСтартовал', status: 1},
  {id: 'no-reads', number: 150, first_name: 'Анна', last_name: 'Иванова', gender: 'female', category_id: 'f'},
  {id: 'start-only', number: 151, first_name: 'Олег', last_name: 'Стартовый', gender: 'male', category_id: 'm', start_time_ms: 1000},
  {id: 'split-only', number: 152, first_name: 'Алексей', last_name: 'Промежуточный'},
].map(member => ({status: 0, start_time_ms: null, finish_time_ms: null, clean_time: null, race_id: 'race', ...member}))
let protocolReads = 0
let exported = false
let legacyResponse = false

function protocol() {
  let place = 0
  const rows = []
  const unfinished = []
  for (const member of members) {
    const row = {...member, member_id: member.id,
      category_name: categories.find(cat => cat.id === member.category_id)?.name,
      status: ({1: 'dns', 2: 'dnf', 3: 'dq'})[member.status] ?? 'ok',
      place: null, gender_place: null, category_place: null, clean_time: null}
    if (member.status) rows.push(row)
    else if (member.finish_time_ms != null || (race.format === 'TimeLimited' && member.id === 'split-only')) {
      row.place = ++place
      row.gender_place = row.gender ? place : null
      row.clean_time = '00:00:01.000'
      if (race.format === 'TimeLimited') row.last_checkpoint_name = 'Круг'
      rows.push(row)
    } else unfinished.push(row)
  }
  const result = {race_id: race.id, race_name: race.name, format: race.format,
    rows, counts: {total: members.length, started: members.length - 1, finished: place}}
  if (!legacyResponse) result.unfinished_rows = unfinished
  return result
}

window.fetch = async (url, options = {}) => {
  const path = new URL(url, location.origin).pathname
  let body
  if (path === '/api/version') body = {version: 'test', build: 'local'}
  else if (path === '/api/events') body = [{id: 'event', name: 'Судейская репетиция', date: '2026-09-08'}]
  else if (path.endsWith('/protocol')) { protocolReads++; body = protocol() }
  else if (path.endsWith('/export-xlsx')) { exported = true; body = {path: 'local-test.xlsx'} }
  else if (path.endsWith('/races')) body = [race]
  else if (path.endsWith('/categories')) body = categories
  else if (path.endsWith('/captures')) body = []
  else if (path.endsWith('/live/status')) body = {running: false}
  else if (path.endsWith('/edits')) {
    const edit = JSON.parse(options.body)
    const member = members.find(item => item.id === edit.entity_id)
    member[edit.field] = edit.value
    body = {recount_needed: true}
  } else if (path.endsWith('/recount')) body = {}
  else if (path.endsWith('/members')) body = members
  else if (path.includes('/members/')) {
    const id = path.split('/members/')[1].split('/')[0]
    body = {...members.find(member => member.id === id), passes: [], manual_results: []}
  } else throw new Error(`Unexpected smoke API request: ${path}`)
  return new Response(JSON.stringify(body), {status: 200, headers: {'Content-Type': 'application/json'}})
}

const query = selector => document.querySelector(selector)
const all = selector => [...document.querySelectorAll(selector)]
const check = (condition, message) => { if (!condition) throw new Error(message) }
async function until(predicate, message) {
  for (let attempt = 0; attempt < 300; attempt++) {
    if (predicate()) return
    await new Promise(resolve => setTimeout(resolve, 10))
  }
  throw new Error(message)
}
function clickText(text) {
  const button = all('button').find(item => item.textContent.trim() === text)
  check(button, `Missing button ${text}`)
  button.click()
}
function input(selector, value, event = 'change') {
  const element = query(selector)
  check(element, `Missing input ${selector}`)
  element.value = value
  element.dispatchEvent(new Event(event, {bubbles: true}))
}
const unfinishedRows = () => all('.unfinished .trow')
async function refresh() {
  const previous = protocolReads
  query('.screen .chips .chip').click()
  await until(() => protocolReads > previous, 'Protocol did not refresh')
  await new Promise(resolve => setTimeout(resolve, 20))
}

async function run() {
  await until(() => query('button.event'), 'Event list did not load')
  query('button.event').click()
  await until(() => query('.awards-cols'), 'Results did not open')
  check(!query('.unfinished'), 'Unfinished list must not appear among winners')
  check(!query('.awards-cols').textContent.includes('Иванова'), 'Unfinished entrant became a winner')
  clickText('Полный протокол')
  await until(() => unfinishedRows().length === 3, 'No-read/start/split entrants missing')
  check(query('.unfinished').textContent.includes('Финишная отметка пока не получена'), 'Missing uncertainty label')
  for (const row of unfinishedRows()) {
    for (const index of [0, 1, 2, 7]) check(row.children[index].textContent === '', 'Unfinished row received place/time')
  }

  input('.proto-tools input', '150', 'input')
  await until(() => unfinishedRows().length === 1, 'Bib filter is not reactive')
  input('.proto-tools select:nth-child(2)', 'male')
  await until(() => unfinishedRows().length === 0, 'Gender filter is not reactive')
  input('.proto-tools select:nth-child(2)', 'female')
  await until(() => unfinishedRows().length === 1, 'Female filter lost unfinished entrant')
  check(all('.proto-tools select:nth-child(3) option').some(option => option.value === 'f'), 'Unfinished-only category unavailable')
  input('.proto-tools select:nth-child(3)', 'm')
  await until(() => unfinishedRows().length === 0, 'Category filter is not reactive')
  input('.proto-tools select:nth-child(3)', 'f')
  await until(() => unfinishedRows().length === 1, 'Unfinished category filter lost row')
  input('.proto-tools input', ' иВаНоВа ', 'input')
  await until(() => unfinishedRows().length === 1, 'Name search lost row')
  input('.proto-tools input', '', 'input')
  input('.proto-tools select:nth-child(2)', 'all')
  input('.proto-tools select:nth-child(3)', 'all')
  await until(() => unfinishedRows().length === 3, 'Reset filters lost rows')

  unfinishedRows()[0].click()
  await until(() => query('.drawer .status-row select'), 'Unfinished participant detail did not open')
  check(query('.drawer .dtitle').textContent.includes('Иванова'), 'Wrong participant detail')
  input('.drawer .status-row select', '2')
  await until(() => unfinishedRows().length === 2, 'Judge DNF did not remove appendix row after refresh')
  check(all('.screen > .table .trow').some(row => row.textContent.includes('Иванова') && row.textContent.includes('DNF')), 'Judge DNF missing from regular protocol')
  query('.drawer .x').click()
  await until(() => !query('.drawer'), 'Drawer did not close')

  members.find(member => member.id === 'start-only').finish_time_ms = 3000
  await refresh()
  check(unfinishedRows().length === 1, 'Late finish did not move entrant out of appendix')
  check(all('.screen > .table .trow').some(row => row.textContent.includes('Стартовый') && row.textContent.includes('00:00:01.000')), 'Late finish missing from regular protocol')
  clickText('Экспорт результата ▾')
  await until(() => query('.menu-item'), 'Excel export menu did not open')
  clickText('Протокол Excel (.xlsx)')
  await until(() => exported && query('.ok-text.msg'), 'Existing Excel export action changed')

  race.format = 'TimeLimited'
  await refresh()
  check(!query('.unfinished'), 'Valid TimeLimited checkpoint outcome remained unfinished')
  check(all('.screen > .table .trow').some(row => row.textContent.includes('Промежуточный') && row.textContent.includes('Круг')), 'TimeLimited result disappeared')
  legacyResponse = true
  await refresh()
  check(!query('.unfinished'), 'Older protocol response did not degrade safely')
  document.documentElement.dataset.protocolSmoke = 'passed'
}

run().catch(error => {
  document.documentElement.dataset.protocolSmoke = 'failed'
  const output = document.createElement('pre')
  output.textContent = error.stack
  document.body.append(output)
})
