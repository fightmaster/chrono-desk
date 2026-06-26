<script>
  import {createEventDispatcher} from 'svelte'
  import {call, fmtTime, fmtDateTime, msToInput, inputToMs, timeStrToMs, memberMatches} from './api.js'

  export let eventId
  export let races = []
  export let categories = []          // full catalog (resolves a current off-list group)
  export let members = []
  export let reloadToken = 0
  export let memberId = null          // normal open (known participant)
  export let capture = null           // manual open: { id, time_ms } unbound finish

  const dispatch = createEventDispatcher()
  const statuses = [
    {value: 0, label: 'OK'},
    {value: 1, label: 'DNS — не стартовал'},
    {value: 2, label: 'DNF — не финишировал'},
    {value: 3, label: 'DSQ — дисквалифицирован'},
  ]

  const openedAsCapture = !!capture
  // Latches true for a manual finish — a fresh capture OR a reopened member that
  // already has one — and STAYS true through an unbind→reassign so the editor
  // doesn't vanish mid-flow.
  let manualMode = openedAsCapture

  // Effective member: the known one (normal) or the one bound to the capture.
  let boundMemberId = memberId
  let bound = !!memberId
  let createdResultId = null // effective manual row in play (for time-edit / re-bind)
  let reassignId = null      // the ONE original row to drop once a reassignment binds

  // Editable finish time string for the manual form (HH:MM:SS.mmm) plus the date
  // anchor used to parse it back to absolute ms.
  let timeStr = capture ? fmtTime(capture.time_ms) : ''
  let timeSeeded = openedAsCapture
  let baseMs = capture ? capture.time_ms : null
  let bindQuery = ''

  let data = null   // passes + derived result
  let member = null // registration fields
  let error = ''
  let onlyHits = true

  // A manual finish already attached to the bound member (reopen case): expose
  // the same time-edit / reassign controls as a fresh capture. Skipped in
  // capture mode, where the unbound capture itself is the source of truth.
  // The EFFECTIVE finish is the FIRST manual row — recount is first-wins
  // (recount.go applies the first per member, time_ms then id, matching run5).
  // Edits/reassign touch ONLY this row, never any sibling.
  $: existingManual = (!openedAsCapture && data && data.manual_results && data.manual_results.length)
    ? data.manual_results[0] : null
  $: if (existingManual) {
    manualMode = true                       // latch — survives unbind→reassign
    createdResultId = existingManual.id      // the row to edit/delete
  }
  $: if (baseMs === null && existingManual) baseMs = existingManual.time_ms
  $: if (!timeSeeded && existingManual) { timeStr = fmtTime(existingManual.time_ms); timeSeeded = true }

  $: bindMatches = bindQuery.trim()
    ? members.filter(m => memberMatches(m, bindQuery.trim().toLowerCase())).slice(0, 6)
    : []

  async function load() {
    if (!boundMemberId) { data = null; member = null; return }
    try {
      data = await call('GET', `/api/events/${eventId}/members/${boundMemberId}/passes`)
      member = await call('GET', `/api/events/${eventId}/members/${boundMemberId}`)
    } catch (e) {
      error = e.message
    }
  }
  // Reload on member change or parent refresh.
  $: load(boundMemberId, reloadToken)

  // Per-race attached category set (run5's category_race) — the groups offered
  // for this participant's race, not the whole catalog.
  let raceCategories = []
  $: loadRaceCategories(member?.race_id, reloadToken)
  async function loadRaceCategories(raceId) {
    if (!raceId) { raceCategories = []; return }
    try { raceCategories = await call('GET', `/api/events/${eventId}/races/${raceId}/categories`) }
    catch (_) { raceCategories = [] }
  }
  // Show the member's current group even if it isn't attached to the race (a
  // legacy/detached assignment shouldn't silently vanish from the dropdown).
  $: categoryOptions = (() => {
    const opts = [...raceCategories]
    const cur = member?.category_id
    if (cur && !opts.some(c => c.id === cur)) {
      const inCatalog = categories.find(c => c.id === cur)
      opts.push(inCatalog ? {id: cur, name: `${inCatalog.name} (не в гонке)`} : {id: cur, name: cur})
    }
    return opts
  })()

  $: passes = data ? data.passes : []
  $: hitCount = passes.filter(p => p.checkpoint_name).length
  $: interesting = passes.filter(p => p.checkpoint_name || p.disabled_at)
  $: shownPasses = onlyHits ? interesting : passes

  async function bindNumber(m) {
    error = ''
    const ms = timeStrToMs(baseMs, timeStr)
    if (ms === null) { error = 'Время финиша в формате ЧЧ:ММ:СС.ммм'; return }
    try {
      const res = await call('POST', `/api/events/${eventId}/members/${m.id}/manual-finish`,
        JSON.stringify({time_ms: ms}))
      // Reopen-reassign: now that the replacement exists, drop the ONE original
      // row (deferred from unbind so a mid-reassign close can't lose the finish).
      // Only this row — any duplicate the member may have is left untouched.
      if (reassignId) {
        try { await call('DELETE', `/api/events/${eventId}/results/${reassignId}`) } catch (_) { /* best-effort */ }
        reassignId = null
      }
      createdResultId = res.result_id
      boundMemberId = m.id
      bound = true
      bindQuery = ''
      dispatch('changed', {recount: true})
      await load()
    } catch (e) {
      error = e.message
    }
  }

  // "Изменить номер": return to search. timeStr/baseMs are kept so the next bind
  // re-applies the same finish time.
  //   • Capture mode — the pending capture is the safety net, so drop the (wrong)
  //     result we just created right now.
  //   • Reopen mode — the manual rows are the ONLY record of this finish, so do
  //     NOT delete: remember them and drop them only once a replacement binds
  //     (bindNumber), so closing/cancelling mid-reassign preserves the original.
  async function unbind() {
    error = ''
    if (openedAsCapture) {
      try {
        if (createdResultId) await call('DELETE', `/api/events/${eventId}/results/${createdResultId}`)
      } catch (e) {
        error = e.message
        return
      }
      dispatch('changed', {recount: true})
    } else {
      reassignId = createdResultId // the current row only; dropped once a replacement binds
    }
    createdResultId = null
    boundMemberId = null
    bound = false
    data = null
    member = null
  }

  // Save an edited finish time. There's no update endpoint and InsertManualResult
  // appends, so re-stamp THIS row only: create the replacement first, then delete
  // the old one (create-then-delete — never a moment with no record on a failure).
  // Touches only the current row; any duplicate the member may have is left alone.
  // No-op when the value is unchanged.
  async function maybeSaveTime() {
    if (!bound || !boundMemberId || !createdResultId) return
    const ms = timeStrToMs(baseMs, timeStr)
    if (ms === null) { error = 'Время финиша в формате ЧЧ:ММ:СС.ммм'; return }
    if (existingManual && ms === existingManual.time_ms) return // blurred unchanged — no-op
    error = ''
    const oldId = createdResultId
    try {
      const res = await call('POST', `/api/events/${eventId}/members/${boundMemberId}/manual-finish`,
        JSON.stringify({time_ms: ms}))
      createdResultId = res.result_id
      try { await call('DELETE', `/api/events/${eventId}/results/${oldId}`) } catch (_) { /* keep the old rather than lose the new */ }
      dispatch('changed', {recount: true})
      await load()
    } catch (e) {
      error = e.message
      await load()
    }
  }

  // Closing finalizes the capture: drop the now-redundant pending capture ONLY
  // when a manual finish is still bound. unbind() leaves the pending capture
  // intact, so picking the wrong participant and closing never loses the time —
  // the capture stays in the list to be re-bound later. Gated on `capture` so a
  // reopened manual finish (no pending capture) never touches the capture list.
  function closeDrawer() {
    if (capture && bound && createdResultId) dispatch('captureBound', {captureId: capture.id})
    dispatch('close')
  }

  async function edit(field, value, entity = 'member', entityId = boundMemberId) {
    error = ''
    try {
      const res = await call('POST', `/api/events/${eventId}/edits`,
        JSON.stringify({entity, entity_id: entityId, field, value}))
      dispatch('changed', {recount: res.recount_needed})
      await load()
    } catch (e) {
      error = e.message
      await load()
    }
  }

  function toggleLog(pass) {
    edit('disabled_at', pass.disabled_at ? null : Date.now(), 'rfid_log', pass.log_id)
  }

  $: title = manualMode
    ? (bound ? `№${data?.number ?? '—'} ${data?.last_name ?? ''} ${data?.first_name ?? ''}`.trim() : 'Ручной финиш')
    : (data ? `№${data.number ?? '—'} ${data.last_name ?? ''} ${data.first_name ?? ''}`.trim() : 'Участник')
</script>

<div class="overlay" on:click={closeDrawer}></div>
<aside class="drawer">
  <div class="dhead">
    <span class="dtitle">{title}</span>
    <button class="x" on:click={closeDrawer}>×</button>
  </div>
  {#if error}<p class="error">{error}</p>{/if}

  {#if manualMode}
    <div class="manual-form" class:is-bound={bound}>
      {#if bound}
        <div class="eyebrow ok-text">Ручной финиш — номер привязан</div>
      {:else}
        <div class="eyebrow amber-text">Ручной финиш — номер не назначен</div>
      {/if}

      <div class="field time-field">
        <span>Время финиша</span>
        <input class="input mono" bind:value={timeStr} placeholder="ЧЧ:ММ:СС.ммм" on:change={maybeSaveTime}/>
      </div>

      <div class="field num-field">
        <span>Номер участника</span>
        {#if bound}
          <div class="bound-chip">
            <span class="mono bn">№{data?.number ?? '—'}</span>
            <span class="bname">{data?.last_name ?? ''} {data?.first_name ?? ''}</span>
            <button class="rebind" on:click={unbind}>Изменить номер</button>
          </div>
        {:else}
          <div class="bind-search">
            <span class="sicon">⌕</span>
            <input class="input" bind:value={bindQuery} placeholder="Поиск: номер или фамилия…"/>
            {#if bindMatches.length}
              <div class="matches">
                {#each bindMatches as m (m.id)}
                  <button class="match" on:click={() => bindNumber(m)}>
                    <span class="mono num">{m.number ?? '—'}</span>
                    <span class="mname">{m.last_name ?? ''} {m.first_name ?? ''}</span>
                  </button>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      </div>

      <p class="faint note">
        {#if bound}
          Если номер привязан к неверному участнику — «Изменить номер» удалит ручной финиш и вернёт поиск. Ниже — карточка участника.
        {:else}
          Пока номер не привязан, известно только время. После привязки появятся регистрационные данные и отсечки.
        {/if}
      </p>
    </div>
  {/if}

  {#if data && member}
    {#if manualMode}<div class="eyebrow card-title">Карточка участника</div>{/if}

    <div class="status-row">
      <div class="field">
        <span>Статус</span>
        <select class="input" value={data.status} on:change={e => edit('status', Number(e.target.value))}>
          {#each statuses as s}<option value={s.value}>{s.label}</option>{/each}
        </select>
      </div>
      <div class="field">
        <span>Старт (ручной)</span>
        <input class="input mono" type="datetime-local" step="1" value={msToInput(data.start_time_ms)}
               on:change={e => edit('start_time_ms', inputToMs(e.target.value))}/>
      </div>
      <div class="finfo mono dim">
        Финиш {data.finish_time_ms ? fmtTime(data.finish_time_ms) : '—'} ·
        Чистое <b class="full">{data.clean_time ?? '—'}</b>
      </div>
    </div>

    <div class="eyebrow">Регистрационные данные</div>
    <div class="reg">
      <div class="field"><span>Фамилия</span>
        <input class="input" value={member.last_name ?? ''} on:change={e => edit('last_name', e.target.value || null)}/></div>
      <div class="field"><span>Имя</span>
        <input class="input" value={member.first_name ?? ''} on:change={e => edit('first_name', e.target.value || null)}/></div>
      <div class="field"><span>Пол</span>
        <select class="input" value={member.gender ?? ''} on:change={e => edit('gender', e.target.value || null)}>
          <option value="">—</option><option value="male">М</option><option value="female">Ж</option>
        </select></div>
      <div class="field"><span>Дата рождения</span>
        <input class="input mono" type="date" value={member.dob ?? ''} on:change={e => edit('dob', e.target.value || null)}/></div>
      <div class="field"><span>Номер</span>
        <input class="input mono" type="number" value={member.number ?? ''}
               on:change={e => edit('number', e.target.value === '' ? null : Number(e.target.value))}/></div>
      <div class="field"><span>Метка (EPC)</span>
        <input class="input mono" value={member.epc ?? ''}
               on:change={e => edit('epc', e.target.value ? e.target.value.toUpperCase() : null)}/></div>
      <div class="field"><span>Группа</span>
        <select class="input" value={member.category_id ?? ''} on:change={e => edit('category_id', e.target.value || null)}>
          <option value="">—</option>
          {#each categoryOptions as c}<option value={c.id}>{c.name}</option>{/each}
        </select></div>
      <div class="field"><span>Гонка</span>
        <select class="input" value={member.race_id} on:change={e => edit('race_id', e.target.value)}>
          {#each races as r}<option value={r.id}>{r.name}</option>{/each}
        </select></div>
    </div>

    <div class="splits-head">
      <span class="sh-title">Отсечки — засчитано {hitCount} из {passes.length}</span>
      {#if passes.length > interesting.length}
        <label class="onlyhits faint">
          <input type="checkbox" bind:checked={onlyHits}/> только засчитанные и отключённые
        </label>
      {/if}
    </div>
    <div class="splits">
      <div class="grid shead">
        <span>Время</span><span>Считыватель</span><span>RSSI</span><span>Засчитано</span><span></span>
      </div>
      {#each shownPasses as p (p.log_id)}
        <div class="grid srow" class:off={p.disabled_at} class:hit={p.checkpoint_name && !p.disabled_at}>
          <span class="mono">{fmtTime(p.time_ms)}</span>
          <span class="mono dim">{p.board}/{p.ant}</span>
          <span class="mono dim">{p.rssi}</span>
          <span class="as">{p.disabled_at ? 'отключено судьёй' : (p.checkpoint_name ?? '—')}</span>
          <button class="icon-btn" class:on={!p.disabled_at} on:click={() => toggleLog(p)}
                  aria-label={p.disabled_at ? 'Включить отсечку' : 'Отключить отсечку'}
                  title={p.disabled_at ? 'Включить отсечку — снова засчитывать' : 'Отключить отсечку — не засчитывать'}>
            <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor"
                 stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M12 3v9"/><path d="M6.4 6.4a8 8 0 1 0 11.2 0"/>
            </svg>
          </button>
        </div>
      {/each}
      {#if !shownPasses.length}
        <p class="faint empty">{passes.length ? 'нет засчитанных отсечек — снимите фильтр' : 'прочтений по этому чипу нет'}</p>
      {/if}
    </div>
  {/if}

  <div class="footer">
    <button class="btn primary" on:click={closeDrawer}>Сохранить</button>
    <button class="btn" on:click={closeDrawer}>Отмена</button>
  </div>
</aside>

<style>
  .overlay { position: fixed; inset: 0; background: rgba(4, 9, 18, .55); z-index: 50; }
  .drawer {
    position: fixed; top: 0; right: 0; bottom: 0; width: 560px; max-width: 92vw;
    background: var(--surface); border-left: 1px solid var(--border2);
    box-shadow: var(--shadow); z-index: 51; overflow-y: auto; padding: 24px 26px;
  }
  .dhead { display: flex; align-items: center; justify-content: space-between; margin-bottom: 20px; }
  .dtitle { font-size: 20px; font-weight: 700; }
  .x { cursor: pointer; background: none; border: none; color: var(--faint); font-size: 22px; line-height: 1; padding: 4px; }

  .manual-form {
    background: var(--surface2); border: 1px solid var(--border2);
    border-radius: 13px; padding: 18px 20px; margin-bottom: 18px;
  }
  .manual-form .eyebrow { margin-bottom: 14px; }
  .time-field { width: 200px; margin-bottom: 16px; }
  .num-field { position: relative; }
  .bind-search { position: relative; }
  .bind-search .input { width: 100%; padding-left: 34px; }
  .bind-search .sicon { position: absolute; left: 12px; top: 50%; transform: translateY(-50%); color: var(--faint); pointer-events: none; }
  .matches {
    position: absolute; top: 46px; left: 0; right: 0; z-index: 60;
    background: var(--surface); border: 1px solid var(--border2);
    border-radius: 11px; box-shadow: var(--shadow); overflow: hidden;
  }
  .match {
    display: flex; align-items: center; gap: 12px; width: 100%; text-align: left;
    padding: 11px 14px; cursor: pointer; background: none; border: none;
    border-bottom: 1px solid var(--border); color: var(--text); font: inherit;
  }
  .match:hover { background: var(--surface2); }
  .match .num { font-weight: 600; color: var(--accent); min-width: 42px; }
  .match .mname { font-weight: 600; }
  .bound-chip {
    display: flex; align-items: center; gap: 12px;
    background: var(--input); border: 1px solid var(--okborder);
    border-radius: 8px; padding: 10px 13px;
  }
  .bound-chip .bn { font-weight: 700; color: var(--accent); }
  .bound-chip .bname { font-weight: 600; flex: 1; }
  .rebind { cursor: pointer; background: none; border: none; color: var(--accent); font: inherit; font-size: 13px; font-weight: 600; }
  .note { font-size: 12.5px; margin: 12px 0 0; }

  .card-title { margin-bottom: 14px; }
  .status-row { display: flex; gap: 16px; margin-bottom: 20px; flex-wrap: wrap; align-items: flex-end; }
  .status-row .field:first-child .input { width: 150px; }
  .finfo { font-size: 13px; padding-bottom: 9px; }
  .finfo .full { color: var(--text); }

  .reg { display: grid; grid-template-columns: 1fr 1fr; gap: 13px; margin: 12px 0 14px; }
  .reg .input { width: 100%; }

  .splits-head { display: flex; align-items: center; justify-content: space-between; margin: 14px 0 10px; flex-wrap: wrap; gap: 8px; }
  .sh-title { font-size: 14px; font-weight: 700; }
  .onlyhits { font-size: 12px; display: flex; align-items: center; gap: 7px; cursor: pointer; }
  .splits { border: 1px solid var(--border); border-radius: 11px; overflow: hidden; }
  .grid { display: grid; grid-template-columns: 124px 1fr 52px 104px 40px; align-items: center; gap: 8px; padding: 10px 14px; }
  .shead { background: var(--surface2); font-size: 11px; font-weight: 700; color: var(--faint); text-transform: uppercase; letter-spacing: .04em; }
  .srow { border-top: 1px solid var(--border); font-size: 13px; }
  .srow .mono:first-child { font-size: 14px; font-weight: 600; }
  .srow.hit { background: var(--okbg); }
  .srow.hit .as { color: var(--ok); font-weight: 600; }
  .srow.off { color: var(--faint); text-decoration: line-through; }
  .icon-btn {
    justify-self: end; display: inline-flex; align-items: center; justify-content: center;
    width: 30px; height: 30px; padding: 0; border: 1px solid var(--border2); border-radius: 8px;
    background: var(--surface); color: var(--faint); cursor: pointer; text-decoration: none;
    transition: color .12s, border-color .12s;
  }
  .icon-btn.on { color: var(--ok); }
  .icon-btn:hover { border-color: var(--accent); color: var(--accent); }
  .empty { padding: 12px 14px; margin: 0; font-style: italic; }

  .footer { display: flex; gap: 10px; margin-top: 22px; }
</style>
