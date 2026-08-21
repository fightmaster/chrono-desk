<script>
  import {createEventDispatcher} from 'svelte'
  import {call, msToInput, inputToMs} from './api.js'

  export let eventId
  export let raceId
  export let reloadToken = 0

  const dispatch = createEventDispatcher()
  const typeNames = {1: 'Старт', 2: 'КП', 3: 'Финиш'}

  let checkpoints = []
  let error = ''

  export async function load() {
    const all = await call('GET', `/api/events/${eventId}/checkpoints`)
    checkpoints = all.filter(c => c.race_id === raceId)
  }

  $: eventId && raceId && reloadToken >= 0 && load()

  async function save(cp, field, value) {
    error = ''
    try {
      const res = await call('POST', `/api/events/${eventId}/edits`,
        JSON.stringify({entity: 'checkpoint', entity_id: cp.id, field, value}))
      dispatch('changed', {recount: res.recount_needed})
    } catch (e) {
      error = `${cp.name}: ${e.message}`
      await load()
    }
  }

  async function removeCheckpoint(cp) {
    if (!confirm(`Удалить чекпоинт «${cp.name || cp.id}»? Его результаты будут пересчитаны.`)) return
    error = ''
    try {
      const res = await call('DELETE', `/api/events/${eventId}/checkpoints/${cp.id}`)
      await load()
      dispatch('changed', {recount: res.recount_needed})
    } catch (e) {
      error = e.message
    }
  }

  let adding = false
  let nf = blank()
  function blank() {
    return {name: '', type: 2, sort: '', board: '', since_offset_seconds: '', sleep_after_prev_seconds: ''}
  }

  async function addCheckpoint() {
    error = ''
    try {
      const res = await call('POST', `/api/events/${eventId}/checkpoints`, JSON.stringify({
        race_id: raceId,
        name: nf.name,
        type: Number(nf.type),
        sort: Number(nf.sort) || 0,
        board: nf.board.trim(),
        since_offset_seconds: numOrNull(nf.since_offset_seconds),
        sleep_after_prev_seconds: numOrNull(nf.sleep_after_prev_seconds),
      }))
      adding = false
      nf = blank()
      await load()
      dispatch('changed', {recount: res.recount_needed})
    } catch (e) {
      error = e.message
    }
  }

  function numOrNull(s) {
    if (s === '' || s === null) return null
    const n = Number(s)
    return Number.isNaN(n) ? null : n
  }
</script>

<div class="head">Чекпоинты <span class="faint">({checkpoints.length})</span></div>
<div class="faint desc">Точки отсечки: считыватель, время активности, офсет от старта и пауза между повторными чтениями.</div>
{#if error}<p class="error">{error}</p>{/if}

<div class="scroll">
  <div class="grid head-row">
    <span>Чекпоинт</span><span>Тип</span><span>Sort</span><span>Считыватель</span>
    <span>Активен с</span><span>Офсет от старта, с</span><span>Сон после отсечки, с</span><span></span>
  </div>

  {#each checkpoints as cp (cp.id)}
    <div class="grid row">
      <span class="cpname">{cp.name}</span>
      <select class="input cell type-sel" value={cp.type}
              on:change={e => save(cp, 'type', Number(e.target.value))}>
        <option value={1}>Старт</option>
        <option value={2}>КП</option>
        <option value={3}>Финиш</option>
      </select>
      <input class="cell mono" type="number" value={cp.sort}
             on:change={e => save(cp, 'sort', numOrNull(e.target.value))}/>
      <input class="cell mono" value={cp.board}
             on:change={e => save(cp, 'board', e.target.value || null)}/>
      <input class="cell mono" type="datetime-local" step="1" value={msToInput(cp.since_ms)}
             on:change={e => save(cp, 'since_ms', inputToMs(e.target.value))}/>
      <input class="cell mono" type="number" value={cp.since_offset_seconds ?? ''}
             on:change={e => save(cp, 'since_offset_seconds', numOrNull(e.target.value))}/>
      <input class="cell mono" type="number" value={cp.sleep_after_prev_seconds ?? ''}
             on:change={e => save(cp, 'sleep_after_prev_seconds', numOrNull(e.target.value))}/>
      <button class="x" title="Удалить чекпоинт" on:click={() => removeCheckpoint(cp)}>✕</button>
    </div>
  {/each}

  {#if adding}
    <div class="grid row">
      <input class="cell" bind:value={nf.name} placeholder="Название"/>
      <select class="input cell type-sel" bind:value={nf.type}>
        <option value={1}>Старт</option>
        <option value={2}>КП</option>
        <option value={3}>Финиш</option>
      </select>
      <input class="cell mono" type="number" bind:value={nf.sort} placeholder="sort"/>
      <input class="cell mono" bind:value={nf.board} placeholder="Feibot:U659"/>
      <span class="cell-na faint">—</span>
      <input class="cell mono" type="number" bind:value={nf.since_offset_seconds}/>
      <input class="cell mono" type="number" bind:value={nf.sleep_after_prev_seconds}/>
      <span class="confirm">
        <button class="ok" title="Добавить" on:click={addCheckpoint}>✓</button>
        <button class="x" title="Отмена" on:click={() => { adding = false; nf = blank() }}>✕</button>
      </span>
    </div>
  {:else}
    <button class="add" on:click={() => adding = true}>+ чекпоинт</button>
  {/if}
</div>

<style>
  .head { font-size: 15px; font-weight: 700; margin-bottom: 4px; }
  .desc { font-size: 13px; margin-bottom: 14px; }
  .scroll { overflow-x: auto; }
  .grid {
    display: grid;
    grid-template-columns: minmax(150px, 1.4fr) 100px 88px 150px 172px 144px 152px 64px;
    gap: 12px; align-items: center; min-width: 960px;
  }
  .head-row {
    padding: 0 0 10px; border-bottom: 1px solid var(--border);
    font-size: 11.5px; font-weight: 700; color: var(--faint);
    text-transform: uppercase; letter-spacing: .04em;
  }
  .row { padding: 12px 0; border-bottom: 1px solid var(--border); }
  .cpname { font-size: 14.5px; font-weight: 600; }
  .cell {
    width: 100%; font: inherit; font-size: 13px;
    background: var(--input); border: 1px solid var(--border);
    border-radius: 7px; padding: 8px 10px; color: var(--text); outline: none;
  }
  .cell.mono { font-family: var(--mono); }
  .type-sel { font-size: 13.5px; font-weight: 600; color: var(--accent); }
  .cell-na { font-family: var(--mono); font-size: 12.5px; }
  .x, .ok {
    background: none; border: none; cursor: pointer;
    font-size: 16px; font-weight: 700; justify-self: center;
  }
  .x { color: var(--bad); }
  .ok { color: var(--ok); }
  .confirm { display: flex; gap: 10px; justify-self: center; }
  .add {
    display: inline-flex; align-items: center; gap: 7px; margin-top: 14px;
    cursor: pointer; padding: 9px 15px; border-radius: 9px;
    border: 1px solid var(--border2); background: var(--surface2);
    color: var(--text); font: inherit; font-size: 13.5px; font-weight: 600;
  }
  .error { color: var(--bad); }
</style>
