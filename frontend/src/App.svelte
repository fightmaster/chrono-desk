<script>
  import {onMount} from 'svelte'
  import {APIBaseURL} from '../wailsjs/go/main/App.js'
  import {setBase, call, msToInput, inputToMs} from './lib/api.js'
  import CheckpointEditor from './lib/CheckpointEditor.svelte'
  import JudgePanel from './lib/JudgePanel.svelte'
  import EditsLog from './lib/EditsLog.svelte'

  let error = ''
  let busy = ''

  let events = []
  let currentEvent = null
  let races = []
  let currentRace = null
  let protocol = null
  let judgeMemberId = null
  let editsLog

  // forms
  let deviceCode = ''
  let csvTimezone = ''

  onMount(async () => {
    try {
      setBase(await APIBaseURL())
      await loadEvents()
    } catch (e) {
      error = `API недоступен: ${e}`
    }
  })

  async function loadEvents() {
    events = await call('GET', '/api/events')
  }

  async function openEvent(ev) {
    currentEvent = ev
    currentRace = null
    protocol = null
    judgeMemberId = null
    csvTimezone = ev.timezone || 'Europe/Moscow'
    await loadRaces()
    if (races.length === 1) await openRace(races[0])
  }

  async function loadRaces() {
    races = await call('GET', `/api/events/${currentEvent.id}/races`)
    if (currentRace) currentRace = races.find(r => r.id === currentRace.id) ?? null
  }

  async function openRace(race) {
    currentRace = race
    judgeMemberId = null
    protocol = await call('GET', `/api/events/${currentEvent.id}/races/${race.id}/protocol`)
  }

  async function refreshProtocol() {
    if (currentRace) {
      protocol = await call('GET', `/api/events/${currentEvent.id}/races/${currentRace.id}/protocol`)
    }
    if (editsLog) await editsLog.load()
  }

  async function recount() {
    if (!currentEvent) return
    error = ''
    busy = 'Пересчёт…'
    try {
      await call('POST', `/api/events/${currentEvent.id}/recount`)
      await refreshProtocol()
    } catch (err) {
      error = `Пересчёт: ${err.message}`
    } finally {
      busy = ''
    }
  }

  // Любая правка: если меняет дериватив — пересчёт, иначе просто перечитать.
  async function onEdited(e) {
    if (e.detail.recount) {
      await recount()
    } else {
      await refreshProtocol()
    }
  }

  async function saveRaceStart(value) {
    error = ''
    busy = 'Сохранение…'
    try {
      await call('POST', `/api/events/${currentEvent.id}/edits`, JSON.stringify({
        entity: 'race', entity_id: currentRace.id, field: 'started_at_ms', value: inputToMs(value),
      }))
      await loadRaces()
      await recount()
    } catch (err) {
      error = `Старт гонки: ${err.message}`
    } finally {
      busy = ''
    }
  }

  async function importEventFile(e) {
    const file = e.target.files[0]
    if (!file) return
    error = ''
    busy = 'Импорт события…'
    try {
      const stats = await call('POST', '/api/events/import', await file.text())
      if (stats.local_edits_reapplied > 0) {
        alert(`Импорт выполнен. Поверх применено локальных правок: ${stats.local_edits_reapplied}`)
      }
      await loadEvents()
      const ev = events.find(x => x.id === stats.event_id)
      if (ev) await openEvent(ev)
    } catch (err) {
      error = `Импорт события: ${err.message}`
    } finally {
      busy = ''
      e.target.value = ''
    }
  }

  async function importCsvFile(e) {
    const file = e.target.files[0]
    if (!file || !currentEvent) return
    if (!deviceCode) {
      error = 'Укажите код считывателя (например, U659)'
      e.target.value = ''
      return
    }
    error = ''
    busy = 'Импорт rfid-логов…'
    try {
      const q = `device=${encodeURIComponent(deviceCode)}&tz=${encodeURIComponent(csvTimezone)}`
      const res = await call('POST', `/api/events/${currentEvent.id}/rfid-import?${q}`, await file.text(), 'text/csv')
      busy = ''
      alert(`Строк: ${res.parsed}, новых: ${res.inserted}, дублей: ${res.duplicates}, ошибок: ${res.errors.length}`)
    } catch (err) {
      error = `Импорт CSV: ${err.message}`
    } finally {
      busy = ''
      e.target.value = ''
    }
  }

  async function exportExcel() {
    if (!currentEvent || !currentRace) return
    error = ''
    busy = 'Экспорт в Excel…'
    try {
      const res = await call('POST', `/api/events/${currentEvent.id}/races/${currentRace.id}/export-xlsx`)
      alert(`Протокол сохранён: ${res.path}`)
    } catch (err) {
      error = `Экспорт Excel: ${err.message}`
    } finally {
      busy = ''
    }
  }

  function statusLabel(s) {
    return {ok: '', dns: 'DNS', dnf: 'DNF', dq: 'DSQ'}[s] ?? s
  }

  $: top3 = protocol ? protocol.rows.filter(r => r.place && r.place <= 3) : []
  $: categoryTop = protocol ? groupCategoryTop(protocol.rows) : []
  $: showCheckpoint = protocol ? protocol.rows.some(r => r.last_checkpoint_name) : false

  function groupCategoryTop(rows) {
    const byCat = new Map()
    for (const r of rows) {
      if (!r.category_place || r.category_place > 3) continue
      const key = r.category_name || r.category_id
      if (!byCat.has(key)) byCat.set(key, [])
      byCat.get(key).push(r)
    }
    return [...byCat.entries()]
  }
</script>

<main>
  <header>
    <h1>Chrono Desk</h1>
    <label class="btn">
      Импорт события (JSON)
      <input type="file" accept=".json" on:change={importEventFile} hidden/>
    </label>
  </header>

  {#if error}<p class="error">{error}</p>{/if}
  {#if busy}<p class="busy">{busy}</p>{/if}

  {#if !currentEvent}
    <section>
      <h2>События</h2>
      {#if events.length === 0}
        <p class="hint">Нет событий. Импортируйте JSON-экспорт из run5.</p>
      {/if}
      <ul class="events">
        {#each events as ev}
          <li><button class="link" on:click={() => openEvent(ev)}>{ev.name}</button>
            <span class="hint">{ev.date}</span></li>
        {/each}
      </ul>
    </section>
  {:else}
    <section>
      <p><button class="link" on:click={() => { currentEvent = null; protocol = null }}>← события</button></p>
      <h2>{currentEvent.name} <span class="hint">{currentEvent.date}</span></h2>

      <div class="toolbar">
        <input placeholder="Код считывателя (U659)" bind:value={deviceCode}/>
        <input placeholder="Таймзона" bind:value={csvTimezone}/>
        <label class="btn">
          Импорт CSV с флешки
          <input type="file" accept=".csv,.txt" on:change={importCsvFile} hidden/>
        </label>
        <button class="btn primary" on:click={recount}>Пересчитать</button>
        {#if currentRace}
          <button class="btn" on:click={exportExcel}>Excel</button>
        {/if}
      </div>

      <div class="races">
        {#each races as race}
          <button class="btn race" class:active={currentRace && currentRace.id === race.id}
                  on:click={() => openRace(race)}>{race.name}</button>
        {/each}
      </div>

      <EditsLog bind:this={editsLog} eventId={currentEvent.id}/>

      {#if currentRace}
        <div class="race-start">
          <label>Старт гонки «{currentRace.name}»
            <input type="datetime-local" step="1" value={msToInput(currentRace.started_at_ms)}
                   on:change={e => saveRaceStart(e.target.value)}/>
          </label>
          <span class="hint">правка времени старта запускает пересчёт</span>
        </div>

        <CheckpointEditor eventId={currentEvent.id} raceId={currentRace.id} on:changed={onEdited}/>
      {/if}

      {#if judgeMemberId}
        <JudgePanel eventId={currentEvent.id} memberId={judgeMemberId}
                    on:changed={onEdited} on:close={() => judgeMemberId = null}/>
      {/if}

      {#if protocol}
        {#if top3.length}
          <h3>Топ-3 — {protocol.race_name}</h3>
          <ol class="top3">
            {#each top3 as r}
              <li>{r.last_name} {r.first_name} — {r.clean_time}</li>
            {/each}
          </ol>
        {/if}

        {#if categoryTop.length}
          <h3>Призёры по группам</h3>
          <div class="cats">
            {#each categoryTop as [cat, rows]}
              <div class="cat">
                <h4>{cat}</h4>
                <ol>
                  {#each rows as r}
                    <li>{r.last_name} {r.first_name} — {r.clean_time}</li>
                  {/each}
                </ol>
              </div>
            {/each}
          </div>
        {/if}

        <h3>Протокол <span class="hint">клик по строке — режим судьи</span></h3>
        <table>
          <thead>
          <tr>
            <th>Место</th><th>Номер</th><th>Участник</th><th>Группа</th>
            <th>Место в группе</th><th>Пол</th>
            {#if showCheckpoint}<th>Чекпоинт</th>{/if}
            <th>Время</th><th>Статус</th>
          </tr>
          </thead>
          <tbody>
          {#each protocol.rows as r (r.member_id)}
            <tr class:nok={r.status !== 'ok'} class:selected={judgeMemberId === r.member_id}
                on:click={() => judgeMemberId = r.member_id}>
              <td>{r.place ?? '—'}</td>
              <td>{r.number ?? ''}</td>
              <td class="name">{r.last_name} {r.first_name}</td>
              <td>{r.category_name ?? ''}</td>
              <td>{r.category_place ?? ''}</td>
              <td>{r.gender_place ?? ''}</td>
              {#if showCheckpoint}<td>{r.last_checkpoint_name ?? ''}</td>{/if}
              <td class="time">{r.clean_time ?? ''}</td>
              <td>{statusLabel(r.status)}</td>
            </tr>
          {/each}
          </tbody>
        </table>
      {/if}
    </section>
  {/if}
</main>

<style>
  main {
    max-width: 1100px;
    margin: 0 auto;
    padding: 1.5rem;
    text-align: left;
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  h1 { margin: 0; }

  .hint { color: #9aa5b1; font-weight: normal; font-size: 0.85em; }
  .error { color: #e57373; }
  .busy { color: #ffb74d; }

  .btn {
    display: inline-block;
    padding: 0.4rem 0.9rem;
    border-radius: 4px;
    border: 1px solid #4a5568;
    background: #2d3748;
    color: inherit;
    cursor: pointer;
  }

  .btn.primary { background: #2b6cb0; border-color: #2b6cb0; }
  .btn.race.active { background: #2b6cb0; }

  .link {
    background: none;
    border: none;
    color: #63b3ed;
    cursor: pointer;
    padding: 0;
    font-size: inherit;
  }

  .toolbar {
    display: flex;
    gap: 0.5rem;
    margin: 1rem 0;
    flex-wrap: wrap;
  }

  .toolbar input, .race-start input {
    padding: 0.4rem 0.6rem;
    border-radius: 4px;
    border: 1px solid #4a5568;
    background: #1a202c;
    color: inherit;
  }

  .races { display: flex; gap: 0.5rem; flex-wrap: wrap; margin-bottom: 1rem; }

  .race-start { display: flex; gap: 1rem; align-items: center; margin: 0.7rem 0; }
  .race-start label { display: flex; gap: 0.6rem; align-items: center; }

  .events { list-style: none; padding: 0; }
  .events li { padding: 0.3rem 0; }

  .cats { display: flex; gap: 1.5rem; flex-wrap: wrap; }
  .cat h4 { margin: 0.2rem 0; }

  table { width: 100%; border-collapse: collapse; margin-top: 0.5rem; }
  th, td { padding: 0.35rem 0.6rem; border-bottom: 1px solid #2d3748; text-align: left; }
  th { color: #9aa5b1; font-weight: 600; }
  td.time { font-family: monospace; }
  tbody tr { cursor: pointer; }
  tbody tr:hover { background: #2a3340; }
  tr.nok { color: #718096; }
  tr.selected { background: #2b3c52; }
</style>
