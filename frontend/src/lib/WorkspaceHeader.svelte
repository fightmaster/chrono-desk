<script>
  import {createEventDispatcher} from 'svelte'
  import {theme, toggleTheme} from './theme.js'
  import {memberMatches} from './api.js'

  export let event
  export let view              // 'results' | 'settings' | 'live'
  export let members = []
  export let races = []
  export let liveStatus = {running: false, port: ''}

  const dispatch = createEventDispatcher()

  let query = ''
  $: matches = query.trim()
    ? members.filter(m => memberMatches(m, query.trim().toLowerCase())).slice(0, 6)
    : []

  // Show the participant's distance (a bare chip number tells the judge nothing).
  // Long race names are truncated — the search list rows are narrow.
  $: raceById = Object.fromEntries(races.map(r => [r.id, r.name]))
  function raceLabel(m) {
    const n = raceById[m.race_id]
    if (!n) return ''
    return n.length > 16 ? n.slice(0, 15) + '…' : n
  }

  function pick(m) {
    query = ''
    dispatch('select', m.id)
  }

  $: liveSub = liveStatus.running
    ? `приём · порт ${liveStatus.port || ''}`.trim()
    : 'приём остановлен'
</script>

<div class="bar">
  <button class="back" on:click={() => dispatch('back')}>
    <span class="chev">‹</span>События
  </button>

  <div class="title">
    <span class="name">{event.name}</span>
    <span class="date mono">{event.date}</span>
  </div>

  <div class="seg">
    <button class="seg-item" class:active={view === 'results'} on:click={() => dispatch('navigate', 'results')}>Результаты</button>
    <button class="seg-item" class:active={view === 'settings'} on:click={() => dispatch('navigate', 'settings')}>Настройки</button>
  </div>

  <div class="search">
    <span class="icon">⌕</span>
    <input class="input" bind:value={query}
           placeholder="Поиск участника по событию: фамилия, номер, метка"/>
    {#if matches.length}
      <div class="matches">
        {#each matches as m (m.id)}
          <button class="match" on:click={() => pick(m)}>
            <span class="num mono">{m.number ?? '—'}</span>
            <span class="mname">{m.last_name ?? ''} {m.first_name ?? ''}</span>
            <span class="faint">{raceLabel(m)}</span>
          </button>
        {/each}
      </div>
    {/if}
  </div>

  <div class="spacer"></div>

  <button class="theme" on:click={toggleTheme}>{$theme === 'night' ? '☀ День' : '☾ Ночь'}</button>

  <button class="live" class:on={view === 'live'} on:click={() => dispatch('navigate', 'live')}>
    <span class="dot" class:pulsing={liveStatus.running}></span>
    <span class="livetext">
      <span class="lbl">LIVE</span>
      <span class="sub">{liveSub}</span>
    </span>
  </button>
</div>

<style>
  .bar {
    display: flex; align-items: center; gap: 18px;
    padding: 12px 24px; border-bottom: 1px solid var(--border);
    background: var(--bar);
  }
  .back {
    cursor: pointer; display: flex; align-items: center; gap: 7px;
    color: var(--dim); font: inherit; font-size: 13.5px; font-weight: 600;
    background: none; border: none; flex-shrink: 0;
  }
  .back .chev { font-size: 16px; line-height: 1; }
  .title {
    display: flex; flex-direction: column; flex-shrink: 0;
    border-left: 1px solid var(--border); padding-left: 18px;
  }
  .title .name { font-size: 15.5px; font-weight: 700; line-height: 1.15; }
  .title .date { font-size: 11.5px; color: var(--faint); }
  .seg { flex-shrink: 0; }

  .search { position: relative; flex: 1; max-width: 380px; }
  .search .input { width: 100%; padding-left: 34px; font-size: 13.5px; }
  .search .icon {
    position: absolute; left: 12px; top: 50%; transform: translateY(-50%);
    color: var(--faint); font-size: 14px; pointer-events: none;
  }
  .matches {
    position: absolute; top: 44px; left: 0; right: 0; z-index: 40;
    background: var(--surface); border: 1px solid var(--border2);
    border-radius: 11px; box-shadow: var(--shadow); overflow: hidden;
  }
  .match {
    display: flex; align-items: center; gap: 12px; width: 100%;
    padding: 11px 14px; cursor: pointer; text-align: left;
    background: none; border: none; border-bottom: 1px solid var(--border);
    color: var(--text); font: inherit;
  }
  .match:hover { background: var(--surface2); }
  .match .num { font-weight: 600; color: var(--accent); min-width: 42px; }
  .match .mname { font-weight: 600; flex: 1; }
  .match .faint { font-size: 12px; color: var(--faint); }

  .spacer { flex: 1; }

  .theme {
    cursor: pointer; flex-shrink: 0; font: inherit;
    padding: 9px 13px; border-radius: 9px;
    border: 1px solid var(--border); background: var(--surface2);
    font-size: 13px; font-weight: 600; color: var(--dim);
  }

  .live {
    cursor: pointer; flex-shrink: 0; font: inherit;
    display: flex; align-items: center; gap: 10px;
    padding: 8px 16px; border-radius: 11px;
    border: 1px solid var(--border2); background: var(--surface2); color: var(--text);
    transition: all .12s;
  }
  .live.on { background: var(--live); border-color: var(--live); color: #06210F; }
  .dot {
    width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0;
    background: var(--faint);
  }
  .dot.pulsing { background: var(--live); animation: pulse 1.6s ease-in-out infinite; }
  .live.on .dot.pulsing { background: #06210F; }
  .livetext { display: flex; flex-direction: column; line-height: 1.1; }
  .livetext .lbl { font-size: 14px; font-weight: 700; letter-spacing: .02em; }
  .livetext .sub { font-size: 10.5px; font-weight: 600; opacity: .85; }
</style>
