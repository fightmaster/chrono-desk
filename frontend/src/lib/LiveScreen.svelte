<script>
  import {createEventDispatcher, onMount, onDestroy} from 'svelte'
  import {call, fmtTime, cleanToMs} from './api.js'

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
  let manualMode = 'clean' // 'clean' = чистое время, 'wall' = время суток
  let manualTime = ''
  let manualClean = ''
  let manualError = ''   // ошибки ввода — под строкой формы
  let flash = ''         // всплывающее подтверждение, само исчезает
  let flashTimer = null

  // число реальных прочтений в ленте (без ручных строк) — для кнопки «Загрузить ещё»
  $: chipShown = feed.filter(p => !p.manual).length

  $: found = query.trim() ? members.filter(m => {
    const q = query.trim().toLowerCase()
    return (m.last_name && m.last_name.toLowerCase().includes(q)) ||
      (m.number !== null && String(m.number).includes(q))
  }).slice(0, 8) : []

  function showFlash(msg) {
    flash = msg
    if (flashTimer) clearTimeout(flashTimer)
    flashTimer = setTimeout(() => { flash = '' }, 3500)
  }

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
  onDestroy(() => {
    clearInterval(timer)
    if (flashTimer) clearTimeout(flashTimer)
  })

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
    manualError = ''
    const who = `№${member.number ?? '—'} ${member.last_name} ${member.first_name}`
    let body, label
    if (manualMode === 'clean') {
      const cleanMs = cleanToMs(manualClean)
      if (cleanMs === null) {
        manualError = 'Введите чистое время в формате ЧЧ:ММ:СС.ммм (например 00:47:13.250)'
        return
      }
      if (!confirm(`Ручной финиш: ${who}, чистое время ${manualClean}?`)) return
      body = {clean_ms: cleanMs}
      label = `${who} · ${manualClean}`
    } else {
      const timeMs = manualTime ? new Date(manualTime).getTime() : Date.now()
      if (!confirm(`Ручной финиш: ${who} в ${fmtTime(timeMs)}?`)) return
      body = {time_ms: timeMs}
      label = `${who} · ${fmtTime(timeMs)}`
    }
    try {
      await call('POST', `/api/events/${eventId}/members/${member.id}/manual-finish`, JSON.stringify(body))
      query = ''
      manualTime = ''
      manualClean = ''
      showFlash(`✓ Добавлено: ${label}`)
      dispatch('changed', {recount: false})
      await refresh() // ручной финиш появится строкой в ленте
    } catch (e) {
      manualError = e.message
    }
  }

  async function deleteManual(p) {
    manualError = ''
    const who = `№${p.number ?? '—'} ${p.last_name ?? ''} ${p.first_name ?? ''}`.trim()
    if (!confirm(`Удалить ручной результат: ${who}?`)) return
    try {
      await call('DELETE', `/api/events/${eventId}/results/${p.result_id}`)
      dispatch('changed', {recount: true}) // пересчёт восстановит данные по чипу
      showFlash(`Удалён ручной результат: ${who}`)
      await refresh()
    } catch (e) {
      manualError = e.message
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
    if (p.manual) return 'finish manual'
    if (p.disabled_at) return 'off'
    if (!p.member_id) return 'unknown'
    if (!p.checkpoint_name) return 'skipped'
    if (p.checkpoint_type === 3) return 'finish'
    return ''
  }

  function passLabel(p) {
    if (p.manual) return 'ручной финиш'
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

  <div class="manual-box">
    <div class="mode">
      <label><input type="radio" bind:group={manualMode} value="clean"/> чистое время</label>
      <label><input type="radio" bind:group={manualMode} value="wall"/> время суток</label>
    </div>
    <div class="manual">
      <input class="q" placeholder="Ручной финиш: номер или фамилия…" bind:value={query}/>
      {#if manualMode === 'clean'}
        <input class="clean" placeholder="ЧЧ:ММ:СС.ммм" bind:value={manualClean}
               title="Чистое время от старта, напр. 00:47:13.250"/>
      {:else}
        <input type="datetime-local" step="1" bind:value={manualTime} title="Пусто = текущее время"/>
      {/if}
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
    {#if manualError}<p class="error under">{manualError}</p>{/if}
    {#if flash}<p class="flash">{flash}</p>{/if}
    <p class="dim hint">Ручные финиши попадают в ленту ниже строкой «ручной финиш» — там же их можно удалить.</p>
  </div>

  <div class="feed">
    {#each feed as p (p.log_id)}
      <div class="row {passClass(p)}">
        <span class="time">{fmtTime(p.time_ms)}</span>
        <span class="num">{p.number ?? ''}</span>
        <span class="name">
          {#if p.member_id}{p.last_name} {p.first_name}{:else}<span class="epc">{p.epc}</span>{/if}
        </span>
        <span class="cp">{passLabel(p)}</span>
        <span class="board">
          {#if p.manual}
            <button class="btn small" title="Удалить ручной результат" on:click={() => deleteManual(p)}>✕</button>
          {:else}{p.board}{/if}
        </span>
      </div>
    {/each}
  </div>
  {#if !feed.length}
    <p class="dim center">Прочтений пока нет — лента обновляется каждые 2 секунды</p>
  {:else if chipShown >= feedLimit && feedLimit < 1000}
    <p class="center">
      <button class="btn" on:click={loadMore}>Загрузить ещё (показано {chipShown})</button>
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

  .manual-box { margin: 0.6rem 0; }
  .mode { display: flex; gap: 1.2rem; margin-bottom: 0.4rem; color: #9aa5b1; }
  .mode label { display: flex; gap: 0.35rem; align-items: center; cursor: pointer; }
  .manual { position: relative; display: flex; gap: 0.6rem; }
  .manual .q { flex: 1; max-width: 24rem; }
  .manual .clean { width: 10rem; font-family: monospace; }
  .error.under { margin: 0.4rem 0 0; }
  .hint { font-size: 0.85rem; margin: 0.35rem 0 0; }
  .flash {
    margin: 0.4rem 0 0; padding: 0.4rem 0.8rem; max-width: 32rem;
    background: #1e3a2a; border: 1px solid #2f855a; border-radius: 6px;
    color: #81c784; animation: flash-fade 3.5s ease-out forwards;
  }
  @keyframes flash-fade {
    0% { opacity: 0; transform: translateY(-4px); }
    8%, 80% { opacity: 1; transform: none; }
    100% { opacity: 0; }
  }
  .btn.small { padding: 0.15rem 0.6rem; font-size: 0.85rem; }
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

  /* CSS-grid feed instead of a <table>: keyed {#each} anchor nodes inside a
     <table> get foster-parented into anonymous table boxes, which broke column
     alignment between chip and manual rows. Grid columns are deterministic. */
  .feed { margin-top: 0.4rem; }
  .row {
    display: grid;
    grid-template-columns: 10.5rem 4.5rem 1fr 12rem 8rem;
    align-items: center;
    column-gap: 0.7rem;
    padding: 0.45rem 0.7rem;
    border-bottom: 1px solid #2d3748;
  }
  .row .time { font-family: monospace; font-size: 1.15rem; white-space: nowrap; }
  .row .num { font-weight: 700; font-size: 1.15rem; }
  .row .name { font-size: 1.1rem; word-break: break-word; }
  .row .board { color: #9aa5b1; font-size: 0.85rem; word-break: break-all; }
  .epc { font-family: monospace; color: #ffb74d; }

  .row.finish { background: #1e3a2a; }
  .row.finish .cp { color: #81c784; font-weight: 600; }
  .row.manual { box-shadow: inset 3px 0 0 #63b3ed; }
  .row.manual .cp { color: #63b3ed; }
  .row.manual .cp::before { content: '✎ '; }
  .row.unknown .cp { color: #ffb74d; }
  .row.skipped .cp { color: #e57373; font-weight: 600; }
  .row.off { color: #718096; text-decoration: line-through; }

  .center { text-align: center; margin-top: 2rem; }
  .error { color: #e57373; }
</style>
