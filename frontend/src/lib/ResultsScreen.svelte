<script>
  import {createEventDispatcher} from 'svelte'
  import {call, fmtTime, inputToMs, msToInput} from './api.js'
  import CheckpointEditor from './CheckpointEditor.svelte'
  import AddMemberForm from './AddMemberForm.svelte'

  export let eventId
  export let races = []
  export let categories = []
  export let currentRace = null
  export let protocol = null
  export let reloadToken = 0

  const dispatch = createEventDispatcher()

  let tab = 'awards' // awards | protocol | dset
  let exportOpen = false
  let exportMsg = ''
  let error = ''

  const genderNames = {male: 'Мужчины', female: 'Женщины'}

  $: rows = protocol ? protocol.rows : []
  // TimeLimited races report which checkpoint each member last reached in-window
  // — restore that protocol column when the data is present.
  $: showCheckpoint = (protocol && protocol.format === 'TimeLimited') || rows.some(r => r.last_checkpoint_name)
  // Authoritative tallies from the backend (by member status + ranked places):
  // finished = members with a place; started = total − DNS. Falls back to row
  // counts if an older response has no `counts`.
  $: counts = protocol && protocol.counts ? protocol.counts : null
  $: distMeta = currentRace
    ? `Старт ${fmtTime(currentRace.started_at_ms)} · финишировало ` +
      `${counts ? counts.finished : rows.filter(r => r.place != null).length} из ${counts ? counts.started : rows.length}`
    : ''

  // Top-3 per gender for the awards columns; if gender isn't marked, fall back
  // to an overall top-3 shown under «Мужчины». `rows` is passed in so Svelte
  // sees the dependency and schedules these after `rows` is computed (a
  // literal-only arg would make Svelte compute them eagerly, before `rows`).
  $: topMen = top3ByGender(rows, 'male')
  $: topWomen = top3ByGender(rows, 'female')
  function top3ByGender(rows, g) {
    const byGender = rows.filter(r => r.gender === g && r.gender_place && r.gender_place <= 3)
    if (byGender.length) return byGender.sort((a, b) => a.gender_place - b.gender_place)
    if (g === 'male' && !rows.some(r => r.gender)) {
      return rows.filter(r => r.place && r.place <= 3).sort((a, b) => a.place - b.place)
    }
    return []
  }

  // Category podiums.
  $: groups = (() => {
    const byCat = new Map()
    for (const r of rows) {
      if (!r.category_place || r.category_place > 3) continue
      const key = r.category_name || r.category_id
      if (!byCat.has(key)) byCat.set(key, [])
      byCat.get(key).push(r)
    }
    return [...byCat.entries()].map(([name, list]) =>
      [name, list.sort((a, b) => a.category_place - b.category_place)])
  })()

  function name(r) { return `${r.last_name ?? ''} ${r.first_name ?? ''}`.trim() }

  async function exportExcel() {
    exportOpen = false
    exportMsg = ''
    error = ''
    if (!currentRace) return
    try {
      const res = await call('POST', `/api/events/${eventId}/races/${currentRace.id}/export-xlsx`)
      exportMsg = `Протокол сохранён: ${res.path}`
    } catch (err) {
      error = `Экспорт Excel: ${err.message}`
    }
  }

  // ---- distance settings ----
  async function saveRaceStart(value) {
    error = ''
    try {
      await call('POST', `/api/events/${eventId}/edits`, JSON.stringify({
        entity: 'race', entity_id: currentRace.id, field: 'started_at_ms', value: inputToMs(value),
      }))
      dispatch('changed', {recount: true, reloadRaces: true})
    } catch (err) {
      error = `Старт гонки: ${err.message}`
    }
  }

  async function saveTop3(checked) {
    error = ''
    try {
      await call('POST', `/api/events/${eventId}/edits`, JSON.stringify({
        entity: 'race', entity_id: currentRace.id,
        field: 'category_excludes_top_by_gender', value: checked ? 1 : 0,
      }))
      dispatch('changed', {recount: true, reloadRaces: true})
    } catch (err) {
      error = `Настройка групп: ${err.message}`
    }
  }

  // Per-race attached category set (run5's category_race). Editable here: the
  // judge attaches an existing catalog group to this distance or detaches one.
  let raceCats = []
  let catError = ''
  $: if (currentRace) loadRaceCats(currentRace.id, reloadToken)
  async function loadRaceCats(raceId) {
    try { raceCats = await call('GET', `/api/events/${eventId}/races/${raceId}/categories`) }
    catch (_) { raceCats = [] }
  }
  // Catalog groups not yet attached to this race — the "add" picker.
  $: availableCats = categories.filter(c => !raceCats.some(rc => rc.id === c.id))

  async function attachCategory(categoryId) {
    if (!categoryId) return
    catError = ''
    try {
      await call('POST', `/api/events/${eventId}/races/${currentRace.id}/categories`,
        JSON.stringify({category_id: categoryId}))
      await loadRaceCats(currentRace.id)
      dispatch('changed', {recount: false}) // no recount; refresh dropdowns elsewhere
    } catch (e) { catError = e.message }
  }
  async function detachCategory(categoryId) {
    catError = ''
    try {
      await call('DELETE', `/api/events/${eventId}/races/${currentRace.id}/categories/${categoryId}`)
      await loadRaceCats(currentRace.id)
      dispatch('changed', {recount: false})
    } catch (e) { catError = e.message }
  }
</script>

<div class="screen">
  <div class="chips">
    {#each races as r (r.id)}
      <button class="chip" class:active={currentRace && currentRace.id === r.id}
              on:click={() => dispatch('selectRace', r)}>{r.name}</button>
    {/each}
  </div>

  {#if currentRace}
    <div class="dist-head">
      <div class="dist-title">
        <span class="dname">{currentRace.name}</span>
        <span class="dmeta mono faint">{distMeta}</span>
      </div>
      <div class="head-actions">
        <AddMemberForm {eventId} {races} defaultRaceId={currentRace.id}
                       on:changed={e => dispatch('changed', e.detail)}/>
        <div class="export">
          <button class="btn primary" on:click={() => exportOpen = !exportOpen}>
            Экспорт результата <span style="opacity:.7">▾</span>
          </button>
          {#if exportOpen}
            <div class="menu">
              <button class="menu-item" on:click={exportExcel}>Протокол Excel (.xlsx)</button>
            </div>
          {/if}
        </div>
      </div>
    </div>

    {#if error}<p class="error">{error}</p>{/if}
    {#if exportMsg}<p class="ok-text msg">{exportMsg}</p>{/if}

    <div class="seg tabs">
      <button class="seg-item" class:active={tab === 'awards'} on:click={() => tab = 'awards'}>Призёры · грамоты</button>
      <button class="seg-item" class:active={tab === 'protocol'} on:click={() => tab = 'protocol'}>Полный протокол</button>
      <button class="seg-item" class:active={tab === 'dset'} on:click={() => tab = 'dset'}>Настройки дистанции</button>
    </div>

    {#if tab === 'awards'}
      <div class="awards-cols">
        {#each [['male', topMen], ['female', topWomen]] as [g, list]}
          <div class="card">
            <div class="eyebrow col-title">Абсолют · {genderNames[g]}</div>
            {#each list as r (r.member_id)}
              <div class="podium-row">
                <span class="medal m{r.gender_place ?? r.place}">{r.gender_place ?? r.place}</span>
                <span class="pname">{name(r)}</span>
                <span class="ptime mono dim">{r.clean_time ?? ''}</span>
              </div>
            {/each}
            {#if !list.length}<p class="faint empty">нет данных</p>{/if}
          </div>
        {/each}
      </div>

      <div class="eyebrow groups-title">Призёры по группам</div>
      <div class="groups">
        {#each groups as [cat, list] (cat)}
          <div class="card group">
            <div class="gname">{cat}</div>
            {#each list as r (r.member_id)}
              <div class="g-row">
                <span class="gplace mono faint">{r.category_place}</span>
                <span class="pname">{name(r)}</span>
                <span class="ptime mono dim">{r.clean_time ?? ''}</span>
              </div>
            {/each}
          </div>
        {/each}
        {#if !groups.length}<p class="faint">Возрастных групп с призёрами нет.</p>{/if}
      </div>

    {:else if tab === 'protocol'}
      <p class="faint hint">Клик по строке — карточка участника и режим судьи.</p>
      <div class="table" class:cp={showCheckpoint}>
        <div class="thead">
          <span>Место</span><span>Номер</span><span>Участник</span><span>Группа</span>
          <span>Место в гр.</span><span>Пол</span><span>Время</span>
          {#if showCheckpoint}<span>Чекпоинт</span>{/if}
          <span>Статус</span>
        </div>
        {#each rows as r (r.member_id)}
          <button class="trow" class:nok={r.status !== 'ok'}
                  on:click={() => dispatch('openMember', r.member_id)}>
            <span class="mono dim">{r.place ?? '—'}</span>
            <span class="mono num">{r.number ?? ''}</span>
            <span class="pname">{name(r)}</span>
            <span class="dim sm">{r.category_name ?? ''}</span>
            <span class="mono dim">{r.category_place ?? ''}</span>
            <span class="mono faint">{r.gender_place ?? ''}</span>
            <span class="mono ptime">{r.clean_time ?? ''}</span>
            {#if showCheckpoint}<span class="dim sm">{r.last_checkpoint_name ?? ''}</span>{/if}
            <span class="status">{({dns: 'DNS', dnf: 'DNF', dq: 'DSQ'})[r.status] ?? ''}</span>
          </button>
        {/each}
      </div>

    {:else if tab === 'dset'}
      <div class="dset">
        <div class="card">
          <div class="eyebrow">Старт гонки</div>
          <div class="start-row">
            <input class="input mono" type="datetime-local" step="1"
                   value={msToInput(currentRace.started_at_ms)}
                   on:change={e => saveRaceStart(e.target.value)}/>
            <span class="faint">Правка времени старта запускает автопересчёт этой дистанции.</span>
          </div>
        </div>

        <label class="card check">
          <input type="checkbox" checked={currentRace.category_excludes_top_by_gender}
                 on:change={e => saveTop3(e.target.checked)}/>
          <span class="check-text">
            <span class="ct-main">Топ-3 абсолюта (М/Ж) не занимает места в группах</span>
            <span class="faint">Призёры абсолюта исключаются из распределения мест внутри возрастных групп.</span>
          </span>
        </label>

        <div class="card">
          <CheckpointEditor {eventId} raceId={currentRace.id} {reloadToken}
                            on:changed={e => dispatch('changed', e.detail)}/>
        </div>

        <div class="card">
          <div class="eyebrow">Возрастные группы</div>
          {#if catError}<p class="error">{catError}</p>{/if}
          <div class="cat-chips">
            {#each raceCats as c (c.id)}
              <span class="chip cat">
                {c.name}
                <button class="chip-x" title="Убрать из дистанции" on:click={() => detachCategory(c.id)}>×</button>
              </span>
            {/each}
            {#if !raceCats.length}<span class="faint">К дистанции не привязано ни одной группы.</span>{/if}
          </div>
          {#if availableCats.length}
            <div class="cat-add">
              <select class="input" on:change={e => { attachCategory(e.target.value); e.target.value = '' }}>
                <option value="">+ добавить из каталога…</option>
                {#each availableCats as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
              </select>
            </div>
          {/if}
        </div>
      </div>
    {/if}
  {:else}
    <p class="faint">У события нет дистанций.</p>
  {/if}
</div>

<style>
  .screen { padding: 18px 24px 60px; }
  .chips { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 18px; }

  .dist-head {
    display: flex; align-items: flex-end; justify-content: space-between;
    gap: 16px; margin-bottom: 16px; flex-wrap: wrap;
  }
  .dist-title { display: flex; flex-direction: column; gap: 6px; }
  .dname { font-size: 24px; font-weight: 700; letter-spacing: -.01em; }
  .dmeta { font-size: 13px; }
  .head-actions { display: flex; gap: 10px; align-items: flex-start; }
  .export { position: relative; }
  .menu {
    position: absolute; right: 0; top: 46px; z-index: 30; min-width: 220px;
    background: var(--surface); border: 1px solid var(--border2);
    border-radius: 11px; box-shadow: var(--shadow); overflow: hidden;
  }
  .menu-item {
    display: block; width: 100%; text-align: left; cursor: pointer;
    padding: 11px 14px; background: none; border: none; color: var(--text);
    font: inherit; font-size: 13.5px;
  }
  .menu-item:hover { background: var(--surface2); }
  .msg { margin: 0 0 12px; font-size: 13px; }

  .tabs { margin-bottom: 22px; }

  /* awards */
  .awards-cols { display: grid; grid-template-columns: 1fr 1fr; gap: 18px; margin-bottom: 26px; }
  .col-title { margin-bottom: 14px; letter-spacing: .07em; }
  .podium-row { display: flex; align-items: center; gap: 14px; padding: 11px 0; border-bottom: 1px solid var(--border); }
  .podium-row:last-child { border-bottom: none; }
  .medal {
    width: 30px; height: 30px; border-radius: 50%; flex-shrink: 0;
    display: flex; align-items: center; justify-content: center;
    font-family: var(--mono); font-weight: 700; font-size: 14px; color: #1A1205;
    background: var(--border2);
  }
  .medal.m1 { background: #E6B450; }
  .medal.m2 { background: #AFB9C6; }
  .medal.m3 { background: #C58B5A; }
  .pname { font-size: 17px; font-weight: 600; flex: 1; }
  .ptime { font-size: 16px; }
  .empty { margin: 8px 0 0; }

  .groups-title { letter-spacing: .07em; margin-bottom: 14px; }
  .groups { display: grid; grid-template-columns: repeat(auto-fill, minmax(310px, 1fr)); gap: 14px; }
  .card.group { padding: 16px 18px; border-radius: 13px; }
  .gname { font-size: 15px; font-weight: 700; margin-bottom: 10px; color: var(--accent); }
  .g-row { display: flex; align-items: center; gap: 11px; padding: 7px 0; }
  .g-row .gplace { font-weight: 600; min-width: 18px; }
  .g-row .pname { font-size: 15px; }
  .g-row .ptime { font-size: 13.5px; }

  /* protocol */
  .hint { font-size: 12.5px; margin: 0 0 10px; }
  .table { border: 1px solid var(--border); border-radius: 13px; overflow: hidden; }
  .thead, .trow {
    display: grid;
    grid-template-columns: 64px 70px 1fr 120px 100px 60px 130px 70px;
    align-items: center; gap: 8px; padding: 12px 18px;
  }
  .table.cp .thead, .table.cp .trow {
    grid-template-columns: 64px 70px 1fr 120px 100px 60px 130px 130px 70px;
  }
  .thead {
    background: var(--surface2);
    font-size: 12px; font-weight: 700; color: var(--faint);
    text-transform: uppercase; letter-spacing: .05em;
  }
  .trow {
    width: 100%; text-align: left; cursor: pointer;
    border: none; border-top: 1px solid var(--border);
    background: none; color: var(--text); font: inherit; font-size: 14.5px;
  }
  .trow:hover { background: var(--surface2); }
  .trow.nok { color: var(--faint); }
  .trow .num { font-weight: 600; color: var(--accent); }
  .trow .pname { font-weight: 600; font-size: 14.5px; }
  .trow .sm { font-size: 13px; }
  .trow .ptime { font-weight: 600; }
  .trow .status { font-size: 12.5px; font-weight: 700; color: var(--bad); }

  /* distance settings */
  .dset { display: flex; flex-direction: column; gap: 14px; max-width: 1080px; }
  .start-row { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; margin-top: 8px; }
  .start-row .input { font-size: 15px; }
  .card.check { display: flex; align-items: center; gap: 14px; cursor: pointer; }
  .card.check input { width: 18px; height: 18px; flex-shrink: 0; accent-color: var(--accent); }
  .check-text { display: flex; flex-direction: column; gap: 3px; }
  .ct-main { font-size: 15px; font-weight: 600; }
  .cat-chips { display: flex; flex-wrap: wrap; gap: 8px; margin: 10px 0; align-items: center; }
  .chip.cat { display: inline-flex; align-items: center; gap: 7px; padding-right: 7px; }
  .chip-x {
    cursor: pointer; background: none; border: none; color: var(--faint);
    font: inherit; font-size: 16px; line-height: 1; padding: 0 2px;
  }
  .chip-x:hover { color: var(--bad); }
  .cat-add { margin-top: 4px; }
  .cat-add .input { max-width: 280px; }
</style>
