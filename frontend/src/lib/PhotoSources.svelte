<script>
  import {createEventDispatcher, onMount} from 'svelte'
  import {call} from './api.js'

  export let eventId

  const dispatch = createEventDispatcher()

  let status = {running: false, sources: [], photos_count: 0}
  let baseUrl = ''
  let error = ''
  let busy = false

  // Phones serve over the LAN; the desk pulls. last_seen_at/skew_ms come straight
  // from the poller (photomanager.go). Buffer/battery aren't in the contract, so
  // we surface only what the API actually reports — no fabricated fields.
  async function load() {
    try {
      status = await call('GET', `/api/events/${eventId}/photos/status`)
    } catch (e) {
      error = e.message
    }
  }
  onMount(load)

  async function add() {
    const url = baseUrl.trim()
    if (!url) return
    error = ''
    busy = true
    try {
      await call('POST', `/api/events/${eventId}/photos/sources`, JSON.stringify({base_url: normalize(url)}))
      baseUrl = ''
      await load()
      dispatch('changed')
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }

  async function remove(src) {
    error = ''
    try {
      await call('DELETE', `/api/events/${eventId}/photos/sources?base_url=${encodeURIComponent(src.base_url)}`)
      await load()
      dispatch('changed')
    } catch (e) {
      error = e.message
    }
  }

  async function pollNow() {
    error = ''
    busy = true
    try {
      await call('POST', `/api/events/${eventId}/photos/poll`)
      await load()
      dispatch('changed')
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }

  // Default the scheme + port so "192.168.0.50" works as typed.
  function normalize(url) {
    let u = url
    if (!/^https?:\/\//i.test(u)) u = 'http://' + u
    if (!/:\d+(\/|$)/.test(u)) u = u.replace(/\/+$/, '') + ':8080'
    return u.replace(/\/+$/, '')
  }

  function ago(ms) {
    if (!ms) return 'нет данных'
    const s = Math.max(0, Math.round((Date.now() - ms) / 1000))
    if (s < 60) return `${s} с назад`
    const m = Math.round(s / 60)
    if (m < 60) return `${m} мин назад`
    return `${Math.round(m / 60)} ч назад`
  }
  function fresh(ms) {
    return ms && (Date.now() - ms) < 15000
  }
  function fmtSkew(ms) {
    if (ms === null || ms === undefined) return '—'
    const a = Math.abs(ms)
    const sign = ms >= 0 ? '+' : '−'
    return a < 1000 ? `${sign}${a} мс` : `${sign}${(a / 1000).toFixed(2)} с`
  }
</script>

<div class="overlay" on:click={() => dispatch('close')}></div>
<aside class="src-drawer">
  <div class="head">
    <span class="title">Источники фото</span>
    <button class="x" on:click={() => dispatch('close')}>×</button>
  </div>
  <p class="sub">Телефоны-камеры снимают финиш автономно. Десктоп подтягивает кадры по локальной сети — на финише никто не стоит.</p>

  {#if error}<p class="error">{error}</p>{/if}

  <div class="add">
    <input class="input mono" bind:value={baseUrl} placeholder="192.168.0.50  ·  адрес телефона"
           on:keydown={e => e.key === 'Enter' && add()}/>
    <button class="btn primary" on:click={add} disabled={busy}>Добавить</button>
  </div>

  <div class="rows">
    {#each status.sources as p (p.base_url)}
      <div class="card">
        <div class="card-head">
          <span class="dot" class:on={fresh(p.last_seen_at)}></span>
          <span class="label">{p.camera_label || p.base_url}</span>
          <span class="conn" class:ok={fresh(p.last_seen_at)}>{fresh(p.last_seen_at) ? 'на связи' : 'нет связи'}</span>
          <button class="del" on:click={() => remove(p)} title="Убрать источник">×</button>
        </div>
        <div class="grid">
          <div class="cell"><span class="k">Адрес</span><span class="v mono">{p.base_url}</span></div>
          <div class="cell"><span class="k">Камера</span><span class="v mono">{p.camera_label || '—'}</span></div>
          <div class="cell"><span class="k">Посл. кадр</span><span class="v mono">{ago(p.last_seen_at)}</span></div>
          <div class="cell"><span class="k">Рассинхрон</span><span class="v mono" class:warn={Math.abs(p.skew_ms) > 200}>{fmtSkew(p.skew_ms)}</span></div>
        </div>
      </div>
    {/each}
    {#if !status.sources.length}
      <p class="empty faint">Источников нет. Включите «Локальная синхронизация» в приложении Chrono Cam на телефоне и впишите показанный там адрес.</p>
    {/if}
  </div>

  <div class="footer-card">
    <div class="fc-row">
      <span class="fc-stat">Кадров в базе: <b class="mono">{status.photos_count}</b></span>
      <span class="fc-stat">Опрос: <b>{status.running ? 'идёт' : 'остановлен'}</b></span>
    </div>
    <button class="btn" on:click={pollNow} disabled={busy}>Опросить сейчас</button>
    <p class="api faint mono">GET /photos?time_ms=…&amp;tolerance_ms=…</p>
    <p class="api-note faint">Тот же контракт можно открыть на втором ноутбуке — он читает те же кадры по сети.</p>
  </div>
</aside>

<style>
  .overlay { position: fixed; inset: 0; background: rgba(4, 9, 18, .55); z-index: 70; }
  .src-drawer {
    position: fixed; top: 0; right: 0; bottom: 0; width: 480px; max-width: 94vw;
    background: var(--surface); border-left: 1px solid var(--border2);
    box-shadow: var(--shadow); z-index: 71; overflow-y: auto; padding: 24px 26px;
  }
  .head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 6px; }
  .title { font-size: 19px; font-weight: 700; }
  .x { cursor: pointer; background: none; border: none; color: var(--faint); font-size: 22px; line-height: 1; padding: 4px; }
  .sub { font-size: 13px; color: var(--faint); margin: 0 0 18px; line-height: 1.5; }
  .error { color: var(--bad); font-size: 13px; margin: 0 0 12px; }

  .add { display: flex; gap: 8px; margin-bottom: 18px; }
  .add .input { flex: 1; }

  .rows { display: flex; flex-direction: column; gap: 12px; }
  .card { background: var(--surface2); border: 1px solid var(--border); border-radius: 13px; padding: 15px 17px; }
  .card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 13px; }
  .dot { width: 9px; height: 9px; border-radius: 50%; background: var(--faint); flex-shrink: 0; }
  .dot.on { background: var(--live); box-shadow: 0 0 0 3px rgba(34, 197, 94, .2); }
  .label { font-size: 15px; font-weight: 700; flex: 1; }
  .conn { font-size: 12.5px; font-weight: 700; color: var(--bad); }
  .conn.ok { color: var(--ok); }
  .del { cursor: pointer; background: none; border: none; color: var(--faint); font-size: 20px; line-height: 1; padding: 0 2px; }
  .del:hover { color: var(--bad); }

  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 13px 12px; }
  .cell { display: flex; flex-direction: column; gap: 3px; }
  .k { font-size: 10px; color: var(--faint); text-transform: uppercase; letter-spacing: .04em; font-weight: 700; }
  .v { font-size: 13px; }
  .v.warn { color: var(--amber); }

  .empty { font-size: 13px; line-height: 1.5; margin: 4px 0; }

  .footer-card { margin-top: 18px; padding: 16px 17px; border-radius: 13px; background: var(--surface2); border: 1px solid var(--border); }
  .fc-row { display: flex; justify-content: space-between; gap: 10px; margin-bottom: 12px; font-size: 13px; color: var(--dim); }
  .api { font-size: 12px; margin: 12px 0 4px; }
  .api-note { font-size: 12px; margin: 0; line-height: 1.45; }

  .input { background: var(--input); border: 1px solid var(--border2); border-radius: 8px; padding: 10px 12px; color: var(--text); font: inherit; outline: none; }
  .input:focus { border-color: var(--accent); }
  .btn { cursor: pointer; padding: 10px 16px; border-radius: 9px; border: 1px solid var(--border2); background: var(--surface); color: var(--text); font: inherit; font-size: 13.5px; font-weight: 600; }
  .btn.primary { background: var(--accent); color: var(--onaccent); border-color: var(--accent); }
  .btn:disabled { opacity: .55; cursor: default; }
  .mono { font-family: 'IBM Plex Mono', monospace; }
  .faint { color: var(--faint); }
</style>
