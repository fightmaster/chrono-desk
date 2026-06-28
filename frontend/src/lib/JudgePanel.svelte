<script>
  import {createEventDispatcher} from 'svelte'
  import {call, msToInput, inputToMs, fmtTime} from './api.js'

  export let eventId
  export let memberId
  export let races = []
  export let categories = []
  export let reloadToken = 0 // bumped by the parent after a recount/refresh so
                             // an open card never shows stale pass data

  const dispatch = createEventDispatcher()
  const statuses = [
    {value: 0, label: 'OK'},
    {value: 1, label: 'DNS — не стартовал'},
    {value: 2, label: 'DNF — не финишировал'},
    {value: 3, label: 'DSQ — дисквалифицирован'},
  ]

  let data = null
  let member = null // registration fields from the protocol/member row
  let error = ''
  let onlyHits = true // a finish reader re-reads a tag hundreds of times; by
                      // default show only the reads that counted or were disabled

  async function load() {
    if (!eventId || !memberId) return
    data = await call('GET', `/api/events/${eventId}/members/${memberId}/passes`)
    member = await call('GET', `/api/events/${eventId}/members/${memberId}`)
  }

  // Reload when the member changes or the parent signals a refresh/recount.
  $: load(eventId, memberId, reloadToken)

  // Which reads matter: those that produced a result (the linked log) or that a
  // judge disabled. The rest are suppressed re-reads of the same tag.
  $: passes = data ? data.passes : []
  $: hitCount = passes.filter(p => p.checkpoint_name).length
  $: interesting = passes.filter(p => p.checkpoint_name || p.disabled_at)
  $: shownPasses = onlyHits ? interesting : passes

  async function edit(field, value, entity = 'member', entityId = memberId) {
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
    const value = pass.disabled_at ? null : Date.now()
    edit('disabled_at', value, 'rfid_log', pass.log_id)
  }

  async function deleteManual(r) {
    error = ''
    if (!confirm(`Удалить ручной результат в ${fmtTime(r.time_ms)}?`)) return
    try {
      await call('DELETE', `/api/events/${eventId}/results/${r.id}`)
      dispatch('changed', {recount: true}) // пересчёт восстановит данные по чипу
      await load()
    } catch (e) {
      error = e.message
    }
  }
</script>

{#if data}
  <aside class="judge">
    <header>
      <h3>№{data.number ?? '—'} {data.last_name} {data.first_name}</h3>
      <button class="close" on:click={() => dispatch('close')}>✕</button>
    </header>
    {#if error}<p class="error">{error}</p>{/if}

    <div class="controls">
      <label>Статус
        <select value={data.status} on:change={e => edit('status', Number(e.target.value))}>
          {#each statuses as s}
            <option value={s.value}>{s.label}</option>
          {/each}
        </select>
      </label>
      <label>Старт (ручной)
        <input type="datetime-local" step="1" value={msToInput(data.start_time_ms)}
               on:change={e => edit('start_time_ms', inputToMs(e.target.value))}/>
      </label>
      <span class="hint">Финиш: {data.finish_time_ms ? fmtTime(data.finish_time_ms) : '—'}
        · Чистое: {data.clean_time ?? '—'}</span>
    </div>

    {#if member}
      <details class="reg">
        <summary>Регистрационные данные</summary>
        <div class="controls">
          <label>Фамилия
            <input value={member.last_name}
                   on:change={e => edit('last_name', e.target.value || null)}/>
          </label>
          <label>Имя
            <input value={member.first_name}
                   on:change={e => edit('first_name', e.target.value || null)}/>
          </label>
          <label>Пол
            <select value={member.gender ?? ''}
                    on:change={e => edit('gender', e.target.value || null)}>
              <option value="">—</option>
              <option value="male">М</option>
              <option value="female">Ж</option>
            </select>
          </label>
          <label>Дата рождения
            <input type="date" value={member.dob ?? ''}
                   on:change={e => edit('dob', e.target.value || null)}/>
          </label>
          <label>Номер
            <input type="number" value={member.number ?? ''}
                   on:change={e => edit('number', e.target.value === '' ? null : Number(e.target.value))}/>
          </label>
          <label>Метка (EPC)
            <input value={member.epc ?? ''}
                   on:change={e => edit('epc', e.target.value ? e.target.value.toUpperCase() : null)}/>
          </label>
          <label>Группа
            <select value={member.category_id ?? ''}
                    on:change={e => edit('category_id', e.target.value || null)}>
              <option value="">—</option>
              {#each categories as c}<option value={c.id}>{c.name}</option>{/each}
            </select>
          </label>
          <label>Гонка
            <select value={member.race_id}
                    on:change={e => edit('race_id', e.target.value)}>
              {#each races as r}<option value={r.id}>{r.name}</option>{/each}
            </select>
          </label>
        </div>
      </details>
    {/if}

    {#if data.manual_results && data.manual_results.length}
      <h4>Ручные результаты ({data.manual_results.length})</h4>
      <table class="manual">
        <tbody>
        {#each data.manual_results as r (r.id)}
          <tr class="hit">
            <td class="time">{fmtTime(r.time_ms)}</td>
            <td>ручной финиш (без чипа)</td>
            <td><button class="btn small" on:click={() => deleteManual(r)}>Удалить</button></td>
          </tr>
        {/each}
        </tbody>
      </table>
    {/if}

    <div class="passes-head">
      <h4>Отсечки — засчитано {hitCount} из {passes.length}</h4>
      {#if passes.length > interesting.length}
        <label class="check">
          <input type="checkbox" bind:checked={onlyHits}/>
          только засчитанные и отключённые
        </label>
      {/if}
    </div>
    <table>
      <thead>
      <tr><th>Время</th><th>Считыватель</th><th>RSSI</th><th>Засчитано как</th><th></th></tr>
      </thead>
      <tbody>
      {#each shownPasses as p (p.log_id)}
        <tr class:off={p.disabled_at} class:hit={p.checkpoint_name && !p.disabled_at}>
          <td class="time">{fmtTime(p.time_ms)}</td>
          <td>{p.board}/{p.ant}</td>
          <td>{p.rssi}</td>
          <td>{p.disabled_at ? 'отключено судьёй' : (p.checkpoint_name ?? '—')}</td>
          <td>
            <button class="btn small" on:click={() => toggleLog(p)}>
              {p.disabled_at ? 'Включить' : 'Отключить'}
            </button>
          </td>
        </tr>
      {/each}
      {#if !shownPasses.length}
        <tr><td colspan="5" class="empty">
          {passes.length ? 'нет засчитанных отсечек — снимите фильтр, чтобы увидеть все прочтения' : 'прочтений по этому чипу нет'}
        </td></tr>
      {/if}
      </tbody>
    </table>
  </aside>
{/if}

<style>
  .judge {
    border: 1px solid #4a5568;
    border-radius: 6px;
    padding: 1rem;
    margin: 1rem 0;
    background: #232b38;
  }
  header { display: flex; justify-content: space-between; align-items: center; }
  h3, h4 { margin: 0.3rem 0; }
  .close { background: none; border: none; color: #9aa5b1; cursor: pointer; font-size: 1rem; }
  .controls { display: flex; gap: 1.5rem; align-items: end; flex-wrap: wrap; margin: 0.7rem 0; }
  label { display: flex; flex-direction: column; gap: 0.2rem; color: #9aa5b1; font-size: 0.85rem; }
  input { background: #1a202c; color: #e2e8f0; border: 1px solid #4a5568; border-radius: 3px; padding: 0.25rem 0.4rem; }
  table { width: 100%; border-collapse: collapse; }
  th, td { padding: 0.25rem 0.4rem; border-bottom: 1px solid #2d3748; text-align: left; font-size: 0.9rem; }
  td.time { font-family: monospace; }
  tr.off { color: #718096; text-decoration: line-through; }
  tr.off td:last-child, tr.off td:nth-child(4) { text-decoration: none; }
  tr.hit td { background: #1e3a2a; }
  tr.hit td:nth-child(4) { color: #81c784; font-weight: 600; }
  .passes-head { display: flex; justify-content: space-between; align-items: baseline; flex-wrap: wrap; gap: 0.5rem; }
  .passes-head .check { display: flex; gap: 0.35rem; align-items: center; color: #9aa5b1; font-size: 0.85rem; cursor: pointer; }
  td.empty { color: #9aa5b1; font-style: italic; text-align: center; }
  .btn.small { padding: 0.1rem 0.5rem; font-size: 0.8rem; background: #2d3748; border: 1px solid #4a5568; border-radius: 3px; color: inherit; cursor: pointer; }
  .hint { color: #9aa5b1; font-size: 0.85rem; }
  .error { color: #e57373; }
  .reg { margin: 0.5rem 0; }
  .reg summary { cursor: pointer; color: #9aa5b1; font-size: 0.9rem; }
</style>
