<script>
  import {createEventDispatcher} from 'svelte'
  import {theme, toggleTheme} from './theme.js'

  export let events = []

  const dispatch = createEventDispatcher()

  // "N дистанций · M участников" — uses counts the list endpoint provides when
  // available, otherwise just the date line stays.
  function meta(ev) {
    const parts = []
    if (ev.race_count != null) parts.push(`${ev.race_count} ${plural(ev.race_count, 'дистанция', 'дистанции', 'дистанций')}`)
    if (ev.member_count != null) parts.push(`${ev.member_count} ${plural(ev.member_count, 'участник', 'участника', 'участников')}`)
    return parts.join(' · ')
  }
  function plural(n, one, few, many) {
    const m10 = n % 10, m100 = n % 100
    if (m10 === 1 && m100 !== 11) return one
    if (m10 >= 2 && m10 <= 4 && (m100 < 10 || m100 >= 20)) return few
    return many
  }
</script>

<div class="topbar">
  <div class="brand">Chrono Desk</div>
  <div class="actions">
    <button class="theme" on:click={toggleTheme}>{$theme === 'night' ? '☀ День' : '☾ Ночь'}</button>
    <label class="import">
      Импорт события (JSON)
      <input type="file" accept=".json" on:change={e => dispatch('import', e)} hidden/>
    </label>
  </div>
</div>

<div class="wrap">
  <div class="eyebrow">События</div>
  {#if events.length === 0}
    <p class="faint">Нет событий. Импортируйте JSON-экспорт из run5.</p>
  {/if}
  <div class="list">
    {#each events as ev (ev.id)}
      <button class="event" on:click={() => dispatch('open', ev)}>
        <div class="info">
          <span class="name">{ev.name}</span>
          {#if meta(ev)}<span class="faint">{meta(ev)}</span>{/if}
        </div>
        <div class="right">
          <span class="date mono dim">{ev.date}</span>
          <span class="chev faint">›</span>
        </div>
      </button>
    {/each}
  </div>
</div>

<style>
  .topbar {
    display: flex; align-items: center; justify-content: space-between;
    padding: 18px 30px; border-bottom: 1px solid var(--border);
  }
  .brand { font-size: 24px; font-weight: 700; letter-spacing: -.01em; }
  .actions { display: flex; align-items: center; gap: 10px; }
  .theme {
    cursor: pointer; font: inherit; padding: 9px 14px; border-radius: 9px;
    border: 1px solid var(--border); background: var(--surface2);
    font-size: 13.5px; font-weight: 600; color: var(--dim);
  }
  .import {
    cursor: pointer; padding: 9px 16px; border-radius: 9px;
    border: 1px solid var(--border2); background: var(--surface2);
    font-size: 13.5px; font-weight: 600;
  }

  .wrap { padding: 34px 30px; max-width: 760px; }
  .eyebrow { font-size: 13px; letter-spacing: .08em; margin-bottom: 16px; }
  .list { display: flex; flex-direction: column; gap: 10px; }
  .event {
    cursor: pointer; font: inherit; text-align: left;
    display: flex; align-items: center; justify-content: space-between;
    padding: 18px 20px; background: var(--surface);
    border: 1px solid var(--border); border-radius: 13px; color: var(--text);
    transition: filter .12s;
  }
  .event:hover { filter: brightness(1.06); }
  .info { display: flex; flex-direction: column; gap: 5px; }
  .info .name { font-size: 18px; font-weight: 700; }
  .info .faint { font-size: 13px; }
  .right { display: flex; align-items: center; gap: 14px; }
  .right .date { font-size: 13px; }
  .right .chev { font-size: 20px; }
</style>
