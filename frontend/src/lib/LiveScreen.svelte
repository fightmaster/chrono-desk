<script>
  import {createEventDispatcher, onMount, onDestroy} from 'svelte'
  import {call, fmtTime} from './api.js'

  export let eventId
  export let members = []

  const dispatch = createEventDispatcher()

  let status = {running: false, ips: [], port: ''}
  let port = '5084'
  let feed = []
  let feedLimit = 40
  let error = ''
  let timer = null

  // ручной финиш
  let query = ''
  let manualTime = ''

  $: found = query.trim() ? members.filter(m => {
    const q = query.trim().toLowerCase()
    return (m.last_name && m.last_name.toLowerCase().includes(q)) ||
      (m.number !== null && String(m.number).includes(q))
  }).slice(0, 8) : []

  async function refresh() {
    try {
      status = await call('GET', `/api/events/${eventId}/live/status`)
      feed = await call('GET', `/api/events/${eventId}/live/feed?limit=${feedLimit}`)
      if (status.port) port = status.port
    } catch (e) {
      error = e.message
    }
  }

  async function loadMore() {
    feedLimit = Math.min(feedLimit + 60, 1000)
    await refresh()
  }

  onMount(() => {
    refresh()
    timer = setInterval(refresh, 2000)
  })
  onDestroy(() => clearInterval(timer))

  async function start() {
    error = ''
    try {
      status = await call('POST', `/api/events/${eventId}/live/start`, JSON.stringify({port}))
    } catch (e) {
      error = e.message
    }
  }

  async function stop() {
    error = ''
    try {
      status = await call('POST', `/api/events/${eventId}/live/stop`)
    } catch (e) {
      error = e.message
    }
  }

  async function manualFinish(member) {
    error = ''
    const timeMs = manualTime ? new Date(manualTime).getTime() : Date.now()
    if (!confirm(`Ручной финиш: №${member.number ?? '—'} ${member.last_name} ${member.first_name} в ${fmtTime(timeMs)}?`)) return
    try {
      await call('POST', `/api/events/${eventId}/members/${member.id}/manual-finish`,
        JSON.stringify({time_ms: timeMs}))
      query = ''
      manualTime = ''
      dispatch('changed', {recount: false})
      await refresh()
    } catch (e) {
      error = e.message
    }
  }

  function readerClass(r) {
    if (r.age_seconds > 90) return 'lost'
    if (r.age_seconds > 30 || r.battery_percent <= 20) return 'warn'
    return 'good'
  }

  function readerAge(r) {
    if (r.age_seconds < 60) return `${r.age_seconds} с назад`
    return `${Math.floor(r.age_seconds / 60)} мин назад`
  }

  function passClass(p) {
    if (p.disabled_at) return 'off'
    if (!p.member_id) return 'unknown'
    if (!p.checkpoint_name) return 'skipped'
    if (p.checkpoint_type === 3) return 'finish'
    return ''
  }

  function passLabel(p) {
    if (p.disabled_at) return 'отключено судьёй'
    if (!p.member_id) return 'неизвестная метка'
    if (!p.checkpoint_name) return 'не засчитано'
    return p.checkpoint_name
  }
</script>

<section class="live">
  <header>
    <button class="btn" on:click={() => dispatch('close')}>← к протоколу</button>
    <h2>Live · финишный судья</h2>
    <div class="listener">
      {#if status.running}
        <span class="ok">● приём на порту {status.port}</span>
        <button class="btn" on:click={stop}>Остановить</button>
      {:else}
        <input class="port" bind:value={port} placeholder="5084"/>
        <button class="btn primary" on:click={start}>Запустить приём</button>
      {/if}
    </div>
  </header>

  {#if error}<p class="error">{error}</p>{/if}

  <div class="hints">
    {#if status.ips?.length}
      <span>Настройка Feibot (второй сервер): <b>{status.ips[0]}:{status.port || port}</b>
        {#if status.ips.length > 1}<span class="dim"> (или {status.ips.slice(1).join(', ')})</span>{/if}</span>
    {:else}
      <span class="dim">Нет адресов в локальной сети — проверьте Wi-Fi/Ethernet</span>
    {/if}
    <span class="dim">прочтений: {status.received ?? 0} · новых: {status.inserted ?? 0}
      · дублей: {status.duplicates ?? 0} · ошибок: {status.errors ?? 0}</span>
  </div>

  {#if status.readers?.length}
    <div class="readers">
      {#each status.readers as r (r.device)}
        <div class="reader {readerClass(r)}">
          <b>{r.device}</b>
          <span>🔋{r.battery_percent}%</span>
          <span>{r.total_tags_read} прочтений</span>
          <span class="age">{readerAge(r)}</span>
        </div>
      {/each}
    </div>
  {:else if status.running}
    <p class="dim">Ожидание heartbeat от считывателей…</p>
  {/if}

  <div class="manual">
    <input class="q" placeholder="Ручной финиш: номер или фамилия…" bind:value={query}/>
    <input type="datetime-local" step="1" bind:value={manualTime} title="Пусто = текущее время"/>
    {#if found.length}
      <div class="candidates">
        {#each found as m (m.id)}
          <button class="cand" on:click={() => manualFinish(m)}>
            №{m.number ?? '—'} {m.last_name} {m.first_name}
          </button>
        {/each}
      </div>
    {/if}
  </div>

  <table>
    <tbody>
    {#each feed as p (p.log_id)}
      <tr class={passClass(p)}>
        <td class="time">{fmtTime(p.time_ms)}</td>
        <td class="num">{p.number ?? ''}</td>
        <td class="name">
          {#if p.member_id}{p.last_name} {p.first_name}{:else}<span class="epc">{p.epc}</span>{/if}
        </td>
        <td class="cp">{passLabel(p)}</td>
        <td class="board">{p.board}</td>
      </tr>
    {/each}
    </tbody>
  </table>
  {#if !feed.length}
    <p class="dim center">Прочтений пока нет — лента обновляется каждые 2 секунды</p>
  {:else if feed.length >= feedLimit && feedLimit < 1000}
    <p class="center">
      <button class="btn" on:click={loadMore}>Загрузить ещё (показано {feed.length})</button>
    </p>
  {/if}
</section>

<style>
  .live { font-size: 1.05rem; }
  header { display: flex; align-items: center; gap: 1.2rem; flex-wrap: wrap; }
  h2 { margin: 0; flex: 1; }
  .listener { display: flex; gap: 0.6rem; align-items: center; }
  .ok { color: #81c784; }
  .port { width: 5rem; }

  .hints { display: flex; justify-content: space-between; gap: 1rem; flex-wrap: wrap;
    margin: 0.6rem 0; padding: 0.5rem 0.8rem; background: #232b38; border-radius: 6px; }
  .dim { color: #9aa5b1; }

  .readers { display: flex; gap: 0.7rem; flex-wrap: wrap; margin: 0.6rem 0; }
  .reader {
    display: flex; gap: 0.7rem; align-items: baseline;
    padding: 0.45rem 0.9rem; border-radius: 6px; border: 1px solid #4a5568;
    font-size: 1rem;
  }
  .reader b { font-size: 1.1rem; }
  .reader .age { color: #9aa5b1; font-size: 0.85rem; }
  .reader.good { border-color: #2f855a; background: #1e3a2a; }
  .reader.warn { border-color: #b7791f; background: #3a2f1e; }
  .reader.lost { border-color: #c53030; background: #3a1e1e; }
  .reader.lost .age { color: #fc8181; font-weight: 600; }

  .manual { position: relative; display: flex; gap: 0.6rem; margin: 0.6rem 0; }
  .manual .q { flex: 1; max-width: 24rem; }
  .candidates { position: absolute; top: 2.4rem; left: 0; z-index: 5; display: flex;
    flex-direction: column; background: #2d3748; border: 1px solid #4a5568; border-radius: 6px; }
  .cand { text-align: left; padding: 0.45rem 0.9rem; background: none; border: none;
    color: inherit; cursor: pointer; font-size: 1rem; }
  .cand:hover { background: #2b6cb0; }

  input { background: #1a202c; color: inherit; border: 1px solid #4a5568;
    border-radius: 4px; padding: 0.35rem 0.6rem; }
  .btn { padding: 0.4rem 0.9rem; border-radius: 4px; border: 1px solid #4a5568;
    background: #2d3748; color: inherit; cursor: pointer; }
  .btn.primary { background: #2b6cb0; border-color: #2b6cb0; }

  table { width: 100%; border-collapse: collapse; margin-top: 0.4rem; }
  td { padding: 0.45rem 0.7rem; border-bottom: 1px solid #2d3748; }
  td.time { font-family: monospace; font-size: 1.15rem; white-space: nowrap; }
  td.num { font-weight: 700; font-size: 1.15rem; width: 4rem; }
  td.name { font-size: 1.1rem; }
  td.cp { white-space: nowrap; }
  td.board { color: #9aa5b1; font-size: 0.85rem; white-space: nowrap; }
  .epc { font-family: monospace; color: #ffb74d; }

  tr.finish td { background: #1e3a2a; }
  tr.finish td.cp { color: #81c784; font-weight: 600; }
  tr.unknown td.cp { color: #ffb74d; }
  tr.skipped td.cp { color: #e57373; font-weight: 600; }
  tr.off td { color: #718096; text-decoration: line-through; }

  .center { text-align: center; margin-top: 2rem; }
  .error { color: #e57373; }
</style>
