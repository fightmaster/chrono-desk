<script>
  import {call} from './api.js'

  export let eventId

  let changes = []

  export async function load() {
    changes = await call('GET', `/api/events/${eventId}/edits`)
  }

  $: eventId && load()

  const entityNames = {race: 'гонка', checkpoint: 'чекпоинт', member: 'участник', rfid_log: 'лог'}

  function fmt(ts) {
    return new Date(ts).toLocaleString('ru-RU')
  }
</script>

{#if changes.length}
  <details class="log">
    <summary>Локальные правки ({changes.length}) — побеждают при реимпорте</summary>
    <table>
      <thead>
      <tr><th>Когда</th><th>Что</th><th>Поле</th><th>Было</th><th>Стало</th></tr>
      </thead>
      <tbody>
      {#each changes as c (c.id)}
        <tr>
          <td>{fmt(c.changed_at)}</td>
          <td>{entityNames[c.entity] ?? c.entity} {c.entity_id}</td>
          <td>{c.field}</td>
          <td class="val">{c.old_value}</td>
          <td class="val">{c.new_value}</td>
        </tr>
      {/each}
      </tbody>
    </table>
  </details>
{/if}

<style>
  .log { margin: 1rem 0; }
  summary { cursor: pointer; color: #ffb74d; }
  table { width: 100%; border-collapse: collapse; margin-top: 0.5rem; }
  th, td { padding: 0.25rem 0.4rem; border-bottom: 1px solid #2d3748; text-align: left; font-size: 0.85rem; }
  td.val { font-family: monospace; max-width: 14rem; overflow: hidden; text-overflow: ellipsis; }
</style>
