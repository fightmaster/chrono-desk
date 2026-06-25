<script>
  import {createEventDispatcher, onMount, onDestroy} from 'svelte'
  import {call, fmtTime, cleanToMs, timeStrToMs, memberMatches} from './api.js'

  export let eventId
  export let members = []
  export let captures = []            // client-side unbound «Зафиксировать время» rows
  export let liveStatus = {running: false, port: ''}

  const dispatch = createEventDispatcher()

  let status = liveStatus
  let port = liveStatus.port || '5084'
  let feed = []
  let feedLimit = 40
  let error = ''
  let timer = null

  // manual finish by number
  let query = ''
  let mode = 'clean' // 'clean' | 'wall'
  let timeStr = ''
  let manualError = ''
  let flash = ''
  let flashTimer = null

  $: chipShown = feed.filter(p => !p.manual).length
  $: found = query.trim()
    ? members.filter(m => memberMatches(m, query.trim().toLowerCase())).slice(0, 6)
    : []

  function showFlash(msg) {
    flash = msg
    if (flashTimer) clearTimeout(flashTimer)
    flashTimer = setTimeout(() => { flash = '' }, 3500)
  }

  // Heartbeat freshness — the start crew's "is this reader still alive" signal.
  function agoLabel(sec) {
    if (sec == null) return ''
    if (sec < 5) return 'только что'
    if (sec < 60) return `${sec} с назад`
    if (sec < 3600) return `${Math.floor(sec / 60)} мин назад`
    return `${Math.floor(sec / 3600)} ч назад`
  }

  async function refresh() {
    try {
      status = await call('GET', `/api/events/${eventId}/live/status`)
      feed = await call('GET', `/api/events/${eventId}/live/feed?limit=${feedLimit}`)
      if (status.port) port = status.port
      dispatch('status', status)
    } catch (e) {
      error = e.message
    }
  }

  async function loadMore() {
    feedLimit = Math.min(feedLimit + 60, 1000)
    await refresh()
  }

  onMount(() => { refresh(); timer = setInterval(refresh, 2000) })
  onDestroy(() => { clearInterval(timer); if (flashTimer) clearTimeout(flashTimer) })

  async function start() {
    error = ''
    try { status = await call('POST', `/api/events/${eventId}/live/start`, JSON.stringify({port})); dispatch('status', status) }
    catch (e) { error = e.message }
  }
  async function stop() {
    error = ''
    try { status = await call('POST', `/api/events/${eventId}/live/stop`); dispatch('status', status) }
    catch (e) { error = e.message }
  }

  async function manualFinish(member) {
    manualError = ''
    const who = `№${member.number ?? '—'} ${member.last_name ?? ''} ${member.first_name ?? ''}`.trim()
    let body, label
    if (mode === 'clean') {
      const cleanMs = cleanToMs(timeStr)
      if (cleanMs === null) { manualError = 'Чистое время в формате ЧЧ:ММ:СС.ммм (например 00:47:13.250)'; return }
      if (!confirm(`Ручной финиш: ${who}, чистое время ${timeStr}?`)) return
      body = {clean_ms: cleanMs}; label = `${who} · ${timeStr}`
    } else {
      const ms = timeStr.trim() ? timeStrToMs(Date.now(), timeStr) : Date.now()
      if (ms === null) { manualError = 'Время суток в формате ЧЧ:ММ:СС.ммм'; return }
      if (!confirm(`Ручной финиш: ${who} в ${fmtTime(ms)}?`)) return
      body = {time_ms: ms}; label = `${who} · ${fmtTime(ms)}`
    }
    try {
      await call('POST', `/api/events/${eventId}/members/${member.id}/manual-finish`, JSON.stringify(body))
      query = ''; timeStr = ''
      showFlash(`✓ Добавлено: ${label}`)
      dispatch('changed', {recount: false})
      await refresh()
    } catch (e) {
      manualError = e.message
    }
  }

  // «Зафиксировать время» — instant wall-clock capture, no number (client state).
  function capture() {
    dispatch('capture', Date.now())
  }

  async function deleteManual(p) {
    manualError = ''
    const who = `№${p.number ?? '—'} ${p.last_name ?? ''} ${p.first_name ?? ''}`.trim()
    if (!confirm(`Удалить ручной результат: ${who}?`)) return
    try {
      await call('DELETE', `/api/events/${eventId}/results/${p.result_id}`)
      dispatch('changed', {recount: true})
      showFlash(`Удалён ручной результат: ${who}`)
      await refresh()
    } catch (e) { manualError = e.message }
  }

  function passClass(p) {
    if (p.manual) return 'manual'
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
    if (p.checkpoint_type === 3) return 'Финиш'
    return p.checkpoint_name
  }
  function openRow(p) {
    if (p.member_id) dispatch('openMember', p.member_id)
  }
</script>

<div class="screen">
  <div class="topline">
    <div class="title"><span class="lt">Live</span><span class="lt dim">· финишный судья</span></div>
    <div class="ctrl">
      <span class="stats mono dim">прочтений {status.received ?? 0} · новых {status.inserted ?? 0} · дублей {status.duplicates ?? 0} · ошибок {status.errors ?? 0}{#if status.last_read_ms} · последнее {fmtTime(status.last_read_ms)}{/if}</span>
      {#if !status.running}<input class="input mono port" bind:value={port}/>{/if}
      {#if status.running}
        <button class="btn" on:click={stop}>Остановить</button>
      {:else}
        <button class="btn primary" on:click={start}>Запустить приём</button>
      {/if}
    </div>
  </div>

  {#if error}<p class="error">{error}</p>{/if}

  <div class="statusbar">
    <span class="dot" class:pulsing={status.running}></span>
    <span class="dim">
      {status.running ? `Приём на порту ${status.port}` : 'Ожидание heartbeat от считывателей…'}
      {#if status.ips?.length}
        · Feibot (второй сервер): <b class="mono full">{status.ips[0]}:{status.port || port}</b>
        {#if status.ips.length > 1}<span class="faint">(или {status.ips.slice(1).join(', ')})</span>{/if}
      {/if}
    </span>
  </div>

  {#if !status.running && status.last_error && !/cancel|closed|EOF/i.test(status.last_error)}
    <p class="error">Приём остановлен с ошибкой: {status.last_error}</p>
  {/if}

  {#if status.readers?.length}
    <div class="readers">
      {#each status.readers as r (r.device)}
        <span class="reader mono" class:stale={r.age_seconds > 30}>
          {r.device} · 🔋{r.battery_percent}% · меток {r.total_tags_read}{#if r.different_tags_read} ({r.different_tags_read} уник.){/if} · heartbeat {agoLabel(r.age_seconds)}
        </span>
      {/each}
    </div>
  {/if}

  <div class="manual-row">
    <div class="seg">
      <button class="seg-item" class:active={mode === 'clean'} on:click={() => mode = 'clean'}>чистое время</button>
      <button class="seg-item" class:active={mode === 'wall'} on:click={() => mode = 'wall'}>время суток</button>
    </div>
    <div class="q-wrap">
      <input class="input" bind:value={query} placeholder="Ручной финиш: номер или фамилия…"/>
      {#if found.length}
        <div class="cands">
          {#each found as m (m.id)}
            <button class="cand" on:click={() => manualFinish(m)}>
              <span class="mono num">{m.number ?? '—'}</span> {m.last_name ?? ''} {m.first_name ?? ''}
            </button>
          {/each}
        </div>
      {/if}
    </div>
    <input class="input mono time" bind:value={timeStr} placeholder="ЧЧ:ММ:СС.ммм"
           title={mode === 'clean' ? 'Чистое время от старта' : 'Время суток (пусто = по кнопке)'}/>
    <button class="btn amber" on:click={capture}>⏱ Зафиксировать время</button>
  </div>
  <p class="faint cap-note">«Зафиксировать время» ставит астрономическое время сразу, без номера. Потом откройте запись кликом — назначьте номер, поправьте время или удалите.</p>
  {#if manualError}<p class="error">{manualError}</p>{/if}
  {#if flash}<p class="flash">{flash}</p>{/if}

  <div class="feed">
    {#each captures as c (c.id)}
      <div class="row capture" on:click={() => dispatch('openCapture', c)}>
        <span class="time mono">{fmtTime(c.time_ms)}</span>
        <span class="num mono">—</span>
        <span class="name">ручной финиш</span>
        <span class="st amber-text">не привязано</span>
        <button class="del" on:click|stopPropagation={() => dispatch('removeCapture', c.id)}>удалить</button>
      </div>
    {/each}
    {#each feed as p (p.log_id)}
      <div class="row {passClass(p)}" on:click={() => openRow(p)}>
        <span class="time mono">{fmtTime(p.time_ms)}</span>
        <span class="num mono">{p.number ?? ''}</span>
        <span class="name">{#if p.member_id}{p.last_name ?? ''} {p.first_name ?? ''}{:else}<span class="epc mono">{p.epc}</span>{/if}</span>
        <span class="st">{passLabel(p)}</span>
        {#if p.manual}
          <button class="del" on:click|stopPropagation={() => deleteManual(p)}>удалить</button>
        {:else}
          <span class="reader-cell mono faint">{p.board}</span>
        {/if}
      </div>
    {/each}
  </div>

  {#if !feed.length && !captures.length}
    <p class="faint center">Прочтений пока нет — лента обновляется каждые 2 секунды.</p>
  {:else if chipShown >= feedLimit && feedLimit < 1000}
    <p class="center"><button class="btn" on:click={loadMore}>Загрузить ещё (показано {chipShown})</button></p>
  {/if}
</div>

<style>
  .screen { padding: 18px 24px 50px; }
  .topline { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 14px; flex-wrap: wrap; }
  .title { display: flex; align-items: center; gap: 14px; }
  .lt { font-size: 22px; font-weight: 700; }
  .ctrl { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
  .stats { font-size: 13px; }
  .port { width: 78px; text-align: center; }

  .statusbar {
    display: flex; align-items: center; gap: 10px;
    padding: 12px 16px; background: var(--surface2);
    border: 1px solid var(--border); border-radius: 11px; margin-bottom: 14px;
  }
  .statusbar .dim { font-size: 13.5px; }
  .statusbar .full { color: var(--text); }
  .dot { width: 10px; height: 10px; border-radius: 50%; background: var(--faint); flex-shrink: 0; }
  .dot.pulsing { background: var(--live); animation: pulse 1.6s ease-in-out infinite; }
  .readers { display: flex; flex-wrap: wrap; gap: 10px; margin-bottom: 12px; }
  .reader { font-size: 12.5px; color: var(--dim); padding: 5px 10px; border: 1px solid var(--border); border-radius: 8px; }
  .reader.stale { color: var(--amber); border-color: var(--amber); }

  .manual-row { display: flex; align-items: center; gap: 16px; margin-bottom: 10px; flex-wrap: wrap; }
  .q-wrap { position: relative; flex: 1; min-width: 200px; }
  .q-wrap .input { width: 100%; }
  .cands {
    position: absolute; top: 46px; left: 0; z-index: 10; min-width: 280px;
    background: var(--surface); border: 1px solid var(--border2);
    border-radius: 11px; box-shadow: var(--shadow); overflow: hidden;
  }
  .cand {
    display: block; width: 100%; text-align: left; cursor: pointer;
    padding: 11px 14px; background: none; border: none; border-bottom: 1px solid var(--border);
    color: var(--text); font: inherit; font-size: 14px;
  }
  .cand:hover { background: var(--surface2); }
  .cand .num { color: var(--accent); font-weight: 600; }
  .time { width: 150px; }
  .cap-note { font-size: 12px; margin: 0 0 14px; }

  .flash {
    margin: 8px 0; padding: 8px 14px; max-width: 32rem;
    background: var(--okbg); border: 1px solid var(--okborder);
    border-radius: 9px; color: var(--ok); animation: flash-fade 3.5s ease-out forwards;
  }
  @keyframes flash-fade { 0% { opacity: 0; } 8%, 80% { opacity: 1; } 100% { opacity: 0; } }

  .feed { border: 1px solid var(--border); border-radius: 13px; overflow: hidden; }
  .row {
    display: flex; align-items: center; gap: 18px;
    padding: 13px 18px; border-bottom: 1px solid var(--border);
    cursor: pointer; transition: filter .12s;
  }
  .row:hover { filter: brightness(1.08); }
  .row .time { font-size: 21px; font-weight: 600; min-width: 150px; letter-spacing: -.01em; white-space: nowrap; }
  .row .num { font-size: 21px; font-weight: 700; color: var(--accent); min-width: 64px; }
  .row .name { font-size: 18px; font-weight: 600; flex: 1; }
  .row .epc { color: var(--amber); font-size: 15px; }
  .row .st { font-size: 15px; font-weight: 700; min-width: 130px; }
  .row .reader-cell { min-width: 96px; text-align: right; font-size: 12.5px; }
  .del {
    min-width: 96px; text-align: right; cursor: pointer;
    background: none; border: none; color: var(--bad); font: inherit; font-size: 13px; font-weight: 600;
  }
  .row.finish { background: var(--okbg); }
  .row.finish .st { color: var(--ok); }
  .row.capture { background: rgba(244, 183, 64, .12); }
  .row.manual { box-shadow: inset 3px 0 0 var(--accent); }
  .row.manual .st { color: var(--accent); }
  .row.unknown .st { color: var(--amber); }
  .row.skipped .st { color: var(--bad); }
  .row.off { color: var(--faint); text-decoration: line-through; }
  .row:not(.finish):not(.capture):not(.manual) .st { color: var(--dim); }

  .center { text-align: center; margin-top: 20px; }
</style>
