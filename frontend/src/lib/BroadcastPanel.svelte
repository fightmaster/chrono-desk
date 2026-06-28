<script>
  import {call} from './api.js'

  export let eventId

  let status = null
  let busy = ''
  let error = ''
  let copied = ''
  let loaded = false

  // Broadcasting this event = the LAN server is up AND serving our id.
  $: live = status?.running && status.event_id === eventId
  $: otherEvent = status?.running && status.event_id !== eventId

  async function loadStatus() {
    error = ''
    try {
      status = await call('GET', '/api/public/status')
      loaded = true
    } catch (e) { error = e.message }
  }
  $: eventId && !loaded && loadStatus()

  async function start() {
    error = ''; busy = 'Включаем…'
    try {
      status = await call('POST', '/api/public/start', JSON.stringify({event_id: eventId}))
    } catch (e) { error = e.message } finally { busy = '' }
  }

  async function stop() {
    error = ''; busy = 'Выключаем…'
    try {
      status = await call('POST', '/api/public/stop')
    } catch (e) { error = e.message } finally { busy = '' }
  }

  async function copy(url) {
    try {
      await navigator.clipboard.writeText(url)
      copied = url
      setTimeout(() => { if (copied === url) copied = '' }, 1500)
    } catch (_) { /* clipboard may be blocked — the address is shown anyway */ }
  }
</script>

<div class="card">
  <div class="head">
    <div class="info">
      <span class="ctitle">Трансляция результатов по сети</span>
      <span class="faint">
        Раздайте результаты по локальной сети — зрители, гравировка медалей и СММ открывают
        страницу с телефона. Только просмотр: без правок и без даты рождения.
      </span>
    </div>
    {#if live}
      <button class="btn" disabled={!!busy} on:click={stop}>Выключить трансляцию</button>
    {:else}
      <button class="btn primary" disabled={!!busy} on:click={start}>
        {otherEvent ? 'Транслировать это событие' : 'Включить трансляцию'}
      </button>
    {/if}
  </div>

  {#if error}<p class="error">{error}</p>{/if}
  {#if busy}<p class="faint busy">{busy}</p>{/if}

  {#if otherEvent}
    <p class="faint other">Сейчас транслируется другое событие. Нажмите «Транслировать это событие», чтобы переключить.</p>
  {/if}

  {#if live}
    <p class="hint">Подключите устройство к той же сети Wi-Fi и откройте адрес или отсканируйте QR. Если адресов несколько — выберите тот, что в вашей сети (обычно Wi-Fi):</p>
    {#if status.endpoints && status.endpoints.length}
      <div class="eps">
        {#each status.endpoints as ep}
          <div class="ep">
            {#if ep.qr}<img class="qr" src={ep.qr} alt={'QR ' + ep.url} width="150" height="150"/>{/if}
            <div class="ep-info">
              <code class="mono url">{ep.url}</code>
              <button class="btn link" on:click={() => copy(ep.url)}>{copied === ep.url ? 'скопировано' : 'копировать'}</button>
            </div>
          </div>
        {/each}
      </div>
      <p class="faint port-note">Порт: <b>{status.port}</b>. Страница обновляется автоматически по мере пересчёта.</p>
    {:else}
      <p class="error">Не удалось определить адрес в сети — проверьте подключение к Wi-Fi.</p>
    {/if}
  {/if}
</div>

<style>
  .head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  .info { display: flex; flex-direction: column; gap: 4px; }
  .ctitle { font-size: 16px; font-weight: 700; }
  .info .faint { font-size: 13.5px; }
  .busy { font-size: 13px; margin: 10px 0 0; }
  .other { font-size: 13.5px; margin: 12px 0 0; }
  .hint { font-size: 13.5px; margin: 14px 0 10px; color: var(--dim); }
  .eps { display: flex; gap: 16px; flex-wrap: wrap; }
  .ep { display: flex; gap: 12px; align-items: center; padding: 10px 12px; border: 1px solid var(--border); border-radius: 12px; }
  .ep-info { display: flex; flex-direction: column; gap: 6px; align-items: flex-start; }
  .url { font-size: 15px; }
  .port-note { font-size: 12.5px; margin: 12px 0 0; }
  .qr { border-radius: 10px; background: #fff; padding: 8px; flex: 0 0 auto; }
</style>
