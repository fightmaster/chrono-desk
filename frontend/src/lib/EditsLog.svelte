<script>
  import {call, fmtDateTime} from './api.js'

  export let eventId
  export let reloadToken = 0

  let changes = []
  let open = false

  export async function load() {
    changes = await call('GET', `/api/events/${eventId}/edits`)
  }
  $: eventId && reloadToken >= 0 && load()

  const entityNames = {race: 'гонка', checkpoint: 'чекпоинт', member: 'участник', rfid_log: 'лог'}
</script>

<button type="button" class="toggle" aria-expanded={open} on:click={() => open = !open}>
  <span class="arrow amber-text">{open ? '▾' : '▸'}</span>
  <span class="title amber-text">Локальные правки ({changes.length})</span>
  <span class="faint hint">— побеждают при реимпорте с сайта</span>
</button>

{#if open && changes.length}
  <div class="scroll">
    <div class="min">
      <div class="grid head">
        <span>Когда</span><span>Что</span><span>Поле</span><span>Было</span><span>Стало</span>
      </div>
      {#each changes as c (c.id)}
        <div class="grid row">
          <span class="mono dim when">{fmtDateTime(c.changed_at)}</span>
          <span class="what"><span class="dim">{entityNames[c.entity] ?? c.entity} </span><span class="mono ref">{c.entity_id}</span></span>
          <span class="mono dim field">{c.field}</span>
          <span class="mono amber-text val">{c.old_value}</span>
          <span class="mono amber-text val">{c.new_value}</span>
        </div>
      {/each}
    </div>
  </div>
{:else if open}
  <p class="faint empty">Локальных правок нет.</p>
{/if}

<style>
  .toggle {
    display: flex; align-items: center; gap: 12px; width: 100%; padding: 0;
    cursor: pointer; color: inherit; background: none; border: 0; font: inherit;
    text-align: left;
  }
  .arrow { font-size: 14px; }
  .title { font-size: 14px; font-weight: 700; }
  .hint { font-size: 13px; }
  .scroll { margin-top: 16px; overflow-x: auto; }
  .min { min-width: 840px; }
  .grid {
    display: grid;
    grid-template-columns: 160px minmax(180px, 1fr) 150px minmax(170px, 1fr) minmax(170px, 1fr);
    gap: 14px;
  }
  .head {
    padding: 0 0 10px; border-bottom: 1px solid var(--border);
    font-size: 11.5px; font-weight: 700; color: var(--faint);
    text-transform: uppercase; letter-spacing: .04em;
  }
  .row { padding: 11px 0; border-bottom: 1px solid var(--border); align-items: baseline; }
  .when { font-size: 12.5px; }
  .what { font-size: 13.5px; }
  .ref { color: var(--accent); }
  .field { font-size: 13px; }
  .val { font-size: 12.5px; word-break: break-all; }
  .empty { margin: 12px 0 0; }
</style>
