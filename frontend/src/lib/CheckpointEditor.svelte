<script>
  import {createEventDispatcher} from 'svelte'
  import {call, msToInput, inputToMs} from './api.js'

  export let eventId
  export let raceId

  const dispatch = createEventDispatcher()
  const typeNames = {1: 'Старт', 2: 'КП', 3: 'Финиш'}

  let checkpoints = []
  let error = ''

  export async function load() {
    const all = await call('GET', `/api/events/${eventId}/checkpoints`)
    checkpoints = all.filter(c => c.race_id === raceId)
  }

  $: eventId && raceId && load()

  async function save(cp, field, value) {
    error = ''
    try {
      const res = await call('POST', `/api/events/${eventId}/edits`,
        JSON.stringify({entity: 'checkpoint', entity_id: cp.id, field, value}))
      dispatch('changed', {recount: res.recount_needed})
    } catch (e) {
      error = `${cp.name}: ${e.message}`
      await load() // вернуть прежнее значение в форму
    }
  }

  function numOrNull(s) {
    if (s === '' || s === null) return null
    const n = Number(s)
    return Number.isNaN(n) ? null : n
  }
</script>

<details class="cps">
  <summary>Чекпоинты ({checkpoints.length})</summary>
  {#if error}<p class="error">{error}</p>{/if}
  <table>
    <thead>
    <tr>
      <th>Чекпоинт</th><th>Тип</th><th>Sort</th><th>Считыватель</th>
      <th>Активен с</th><th>Офсет от старта, с</th><th>Сон после отсечки, с</th>
    </tr>
    </thead>
    <tbody>
    {#each checkpoints as cp (cp.id)}
      <tr>
        <td>{cp.name}</td>
        <td>{typeNames[cp.type] ?? cp.type}</td>
        <td><input class="num" type="number" value={cp.sort}
                   on:change={e => save(cp, 'sort', numOrNull(e.target.value))}/></td>
        <td><input class="board" value={cp.board}
                   on:change={e => save(cp, 'board', e.target.value || null)}/></td>
        <td><input type="datetime-local" step="1" value={msToInput(cp.since_ms)}
                   on:change={e => save(cp, 'since_ms', inputToMs(e.target.value))}/></td>
        <td><input class="num" type="number" value={cp.since_offset_seconds ?? ''}
                   on:change={e => save(cp, 'since_offset_seconds', numOrNull(e.target.value))}/></td>
        <td><input class="num" type="number" value={cp.sleep_after_prev_seconds ?? ''}
                   on:change={e => save(cp, 'sleep_after_prev_seconds', numOrNull(e.target.value))}/></td>
      </tr>
    {/each}
    </tbody>
  </table>
</details>

<style>
  .cps { margin: 1rem 0; }
  summary { cursor: pointer; color: #9aa5b1; }
  table { width: 100%; border-collapse: collapse; margin-top: 0.5rem; }
  th, td { padding: 0.25rem 0.4rem; border-bottom: 1px solid #2d3748; text-align: left; font-size: 0.9rem; }
  input { background: #1a202c; color: inherit; border: 1px solid #4a5568; border-radius: 3px; padding: 0.15rem 0.3rem; }
  input.num { width: 5.5rem; }
  input.board { width: 8rem; }
  .error { color: #e57373; }
</style>
