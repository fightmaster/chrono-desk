<script>
  import {createEventDispatcher} from 'svelte'
  import {call, fmtDateTime} from './api.js'
  import PacketIssuanceReview from './PacketIssuanceReview.svelte'

  export let eventId

  const dispatch = createEventDispatcher()

  let configOpen = false
  let baseUrl = ''
  let token = ''
  let tokenSet = false
  let lastSyncedAt = null
  let storage = null
  let overwrite = true
  let siteWins = false
  let busy = ''
  let error = ''
  let result = null
  let pullResult = null
  let saved = false
  let loaded = false
  let packetStatus = null
  let packetLAN = null
  let packetInvitation = null
  let packetLabel = 'Планшет выдачи'
  let certificatePath = ''

  async function loadConfig() {
    error = ''
    try {
      const cfg = await call('GET', `/api/events/${eventId}/sync-config`)
      baseUrl = cfg.base_url || ''
      tokenSet = cfg.token_set
      lastSyncedAt = cfg.last_synced_at
      storage = cfg.storage || null
      packetStatus = await call('GET', `/api/events/${eventId}/packet-issuance/site`)
      try {
        packetLAN = await call('GET', `/api/events/${eventId}/packet-issuance/lan`)
      } catch (_) {
        // Site sync remains available when the optional LAN receiver is absent.
        packetLAN = null
      }
      loaded = true
    } catch (e) { error = e.message }
  }

  function fmtBytes(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) return '—'
    if (bytes < 1024) return `${bytes} Б`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} КБ`
    return `${(bytes / (1024 * 1024)).toFixed(1)} МБ`
  }
  $: eventId && !loaded && loadConfig()

  async function saveConfig() {
    error = ''; saved = false
    try {
      await call('PUT', `/api/events/${eventId}/sync-config`,
        JSON.stringify({base_url: baseUrl.trim(), token: token.trim()}))
      token = ''; saved = true
      await loadConfig()
    } catch (e) { error = e.message }
  }

  async function push() {
    error = ''; result = null; pullResult = null
    busy = 'Отправка на сайт…'
    try {
      result = await call('POST', `/api/events/${eventId}/sync`, JSON.stringify({overwrite}))
      await loadConfig()
    } catch (e) { error = `Отправка: ${e.message}` } finally { busy = '' }
  }

  async function pull() {
    error = ''; result = null; pullResult = null
    if (!confirm(siteWins
      ? 'Получить с сайта и взять значения сайта поверх локальных правок?'
      : 'Получить с сайта (локальные правки сохранятся поверх)?')) return
    busy = 'Получение с сайта…'
    try {
      pullResult = await call('POST', `/api/events/${eventId}/sync-pull`, JSON.stringify({overwrite: siteWins}))
      dispatch('pulled')
    } catch (e) { error = `Получение: ${e.message}` } finally { busy = '' }
  }

  async function connectPacketIssuance() {
    error = ''; busy = 'Подключение выдачи пакетов…'
    try {
      packetStatus = await call('POST', `/api/events/${eventId}/packet-issuance/site/connect`, '{}')
    } catch (e) { error = `Выдача пакетов: ${e.message}` } finally { busy = '' }
  }

  async function setPacketLAN(running) {
    error = ''; busy = running ? 'Запуск локальной выдачи…' : 'Остановка локальной выдачи…'
    try {
      packetLAN = await call('POST', `/api/events/${eventId}/packet-issuance/lan/${running ? 'start' : 'stop'}`, '{}')
      if (!running) packetInvitation = null
    } catch (e) { error = `Локальная выдача: ${e.message}` } finally { busy = '' }
  }

  async function createPacketInvitation() {
    error = ''; busy = 'Создание подключения планшета…'; packetInvitation = null
    try {
      packetInvitation = await call('POST', `/api/events/${eventId}/packet-issuance/lan/invitations`,
        JSON.stringify({label: packetLabel.trim()}))
      packetLAN = await call('GET', `/api/events/${eventId}/packet-issuance/lan`)
    } catch (e) { error = `Подключение планшета: ${e.message}` } finally { busy = '' }
  }

  async function revokePacketConnection(connectionId) {
    if (!confirm('Отозвать доступ этого планшета? Уже загруженный список останется на нём.')) return
    error = ''; busy = 'Отзыв подключения…'
    try {
      await call('POST', `/api/events/${eventId}/packet-issuance/lan/connections/${connectionId}/revoke`,
        JSON.stringify({reason: 'Отозвано оператором Chrono Desk'}))
      packetLAN = await call('GET', `/api/events/${eventId}/packet-issuance/lan`)
    } catch (e) { error = `Отзыв планшета: ${e.message}` } finally { busy = '' }
  }

  async function compactPacketFeed() {
    if (!confirm('Архивировать до 100 старых записей транспорта? История сохранится, отставшие планшеты заново сверят список.')) return
    error = ''; busy = 'Архивация журнала выдачи…'
    try {
      await call('POST', `/api/events/${eventId}/packet-issuance/lan/compact`, JSON.stringify({execute: true}))
      packetLAN = await call('GET', `/api/events/${eventId}/packet-issuance/lan`)
    } catch (e) { error = `Архивация выдачи: ${e.message}` } finally { busy = '' }
  }

  async function downloadPacketCA() {
    error = ''; certificatePath = ''; busy = 'Сохранение сертификата…'
    try {
      const data = await call('POST', `/api/events/${eventId}/packet-issuance/lan/ca/export`, '{}')
      certificatePath = data.path
    } catch (e) { error = `Сертификат: ${e.message}` } finally { busy = '' }
  }
</script>

<div class="card">
  <div class="head">
    <div class="info">
      <span class="ctitle">Синхронизация с сайтом run5</span>
      <span class="faint">
        {#if lastSyncedAt}Последняя синхронизация: {fmtDateTime(lastSyncedAt)}. {/if}Локальные правки побеждают при реимпорте.
      </span>
    </div>
    <button class="btn link cfg-toggle" on:click={() => configOpen = !configOpen}>
      {configOpen ? 'свернуть настройки' : 'настройки подключения'}
    </button>
  </div>

  {#if error}<p class="error">{error}</p>{/if}

  {#if configOpen}
    <div class="cfg">
      <div class="field"><span>Адрес сайта</span>
        <input class="input" placeholder="https://run5.example" bind:value={baseUrl}/></div>
      <div class="field"><span>Токен синхронизации</span>
        <input class="input" type="password" placeholder={tokenSet ? '•••••• (задан)' : 'вставьте токен'} bind:value={token}/></div>
      <button class="btn" on:click={saveConfig}>Сохранить</button>
      {#if saved}<span class="ok-text saved">сохранено</span>{/if}
      {#if storage}
        <span class="storage faint" title="Размеры снимаются без checkpoint WAL">
          SQLite: {fmtBytes(storage.database_bytes)} · WAL: {fmtBytes(storage.wal_bytes)} ·
          всего: {fmtBytes(storage.total_bytes)}
        </span>
      {/if}
    </div>
  {/if}

  <div class="actions">
    <label class="check" title="Применить на сайте только явно сделанные офлайн-правки. Чужие точки и отметки не удаляются и не включаются обратно.">
      <input type="checkbox" bind:checked={overwrite}/> применять локальные правки на сайте
    </label>
    <button class="btn primary" disabled={!!busy || !baseUrl || !tokenSet} on:click={push}>Отправить на сайт →</button>
  </div>

  {#if packetStatus?.roster_installed}
    <div class="packet-lan">
      <div class="actions">
        <span class="faint">
          Планшеты в локальной сети: {packetLAN?.running ? `приём включён · ${packetLAN.api_base_url}` : 'приём выключен'}
        </span>
        <button class="btn" disabled={!!busy} on:click={downloadPacketCA}>Сохранить сертификат Desk</button>
        <button class="btn" disabled={!!busy} on:click={() => setPacketLAN(!packetLAN?.running)}>
          {packetLAN?.running ? 'Остановить приём' : 'Включить приём'}
        </button>
      </div>
      {#if certificatePath}
        <p class="ok-text" role="status">Сертификат сохранён: {certificatePath}</p>
      {/if}
      <p class="faint packet-help">
        Кнопка сохраняет файл chrono-desk-ca.crt в «Загрузки» на компьютере.
        Передайте его на каждый планшет и установите как доверенный сертификат:
        он нужен для HTTPS-подключения к этому Desk по локальной сети.
        Затем создайте отдельный QR для планшета.
        Для работы без Интернета сначала подключите планшет QR сайта, затем этим QR Desk: это один список
        с двумя независимыми получателями. Desk принимает изменения по локальному Wi-Fi; конфликты можно
        разобрать здесь без Интернета, тем же журналом, что используется на сайте. Сервер объявляется как
        chrono-desk.local только пока приём включён.
      </p>
      {#if packetLAN?.running}
        <div class="actions">
          <input class="input packet-label" aria-label="Название планшета" maxlength="160" bind:value={packetLabel}/>
          <button class="btn primary" disabled={!!busy || !packetLabel.trim()} on:click={createPacketInvitation}>Создать QR планшета</button>
        </div>
      {/if}
      {#if packetInvitation}
        <div class="packet-invitation">
          <img src={packetInvitation.qr_code} alt="QR подключения планшета к Chrono Desk"/>
          <div><b>QR действует до {packetInvitation.expires_at}</b>
            <p class="faint">Покажите его только нужному волонтёру. После успешного подключения код повторно не показывается.</p></div>
        </div>
      {/if}
      {#if packetLAN?.connections?.length}
        <div class="packet-peers">
          {#each packetLAN.connections as peer}
            <div class="packet-peer">
              <span>{peer.label} · {peer.claimed_at ? 'подключён' : 'ожидает сканирования'}{peer.revoked_at ? ' · отозван' : ''}</span>
              {#if !peer.revoked_at}<button class="btn link" disabled={!!busy} on:click={() => revokePacketConnection(peer.connection_id)}>отозвать</button>{/if}
            </div>
          {/each}
        </div>
      {/if}
      {#if !packetLAN?.running && packetLAN?.retention?.candidate > 0}
        <div class="actions">
          <span class="faint">Старый транспортный журнал: {packetLAN.retention.candidate} записей доступно для архивации.</span>
          <button class="btn" disabled={!!busy} on:click={compactPacketFeed}>Архивировать 100 записей</button>
        </div>
      {/if}
      <PacketIssuanceReview {eventId} on:changed={() => dispatch('pulled')}/>
    </div>
  {/if}

  <div class="actions">
    <span class="faint">
      Выдача пакетов:
      {#if packetStatus?.roster_installed}список сайта установлен
      {:else if packetStatus?.configured}доступ получен, список не установлен
      {:else}не подключена{/if}
      {#if packetStatus?.expires_at} · доступ до {packetStatus.expires_at}{/if}
    </span>
    <button class="btn" disabled={!!busy || !baseUrl || !tokenSet || packetStatus?.roster_installed}
      on:click={connectPacketIssuance}>Подключить выдачу пакетов</button>
  </div>

  <div class="actions">
    <label class="check" title="Взять значения сайта поверх локальных правок.">
      <input type="checkbox" bind:checked={siteWins}/> значения сайта важнее локальных правок
    </label>
    <button class="btn" disabled={!!busy || !baseUrl || !tokenSet} on:click={pull}>← Получить с сайта</button>
    {#if busy}<span class="amber-text">{busy}</span>{/if}
  </div>

  {#if result}
    <div class="result faint">
      Отправлено — логи: <b>{result.sent?.rfid_logs ?? '—'}</b> ·
      правки логов: <b>{result.sent?.rfid_log_edits ?? 0}</b> ·
      ручные: <b>{result.sent?.manual_results ?? 0}</b> ·
      правки участников: <b>{result.sent?.member_edits ?? 0}</b> ·
      новые: <b>{result.sent?.new_members ?? 0}</b> ·
      операции выдачи: <b>{result.packet_issuance?.accepted ?? 0}</b>
    </div>
  {/if}
  {#if pullResult}
    <div class="result faint">
      Получено{pullResult.site_wins ? ' (значения сайта)' : ' (локальные правки сохранены)'} —
      участники: <b>{pullResult.imported?.members ?? 0}</b> ·
      логи: <b>{pullResult.imported?.rfid_logs ?? 0}</b> ·
      правок переиграно: <b>{pullResult.imported?.local_edits_reapplied ?? 0}</b>.
    </div>
  {/if}
</div>

<style>
  .head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  .info { display: flex; flex-direction: column; gap: 4px; }
  .ctitle { font-size: 16px; font-weight: 700; }
  .info .faint { font-size: 13.5px; }
  .cfg-toggle { font-size: 13px; white-space: nowrap; }
  .cfg { display: flex; gap: 14px; align-items: flex-end; flex-wrap: wrap; margin-top: 14px; }
  .cfg .input { min-width: 16rem; }
  .saved { font-size: 13px; align-self: center; }
  .storage { width: 100%; font-size: 12.5px; }
  .actions { display: flex; gap: 14px; align-items: center; flex-wrap: wrap; margin-top: 12px; }
  .check { display: flex; align-items: center; gap: 8px; font-size: 13.5px; color: var(--dim); cursor: pointer; }
  .check input { accent-color: var(--accent); }
  .result { margin-top: 12px; font-size: 13px; }
  .packet-lan { margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--line); }
  .packet-help { margin: 8px 0; font-size: 12.5px; }
  .packet-label { max-width: 20rem; }
  .packet-invitation { display: flex; align-items: center; gap: 14px; margin-top: 12px; }
  .packet-invitation img { width: 190px; height: 190px; background: white; border-radius: 8px; }
  .packet-peers { margin-top: 10px; }
  .packet-peer { display: flex; justify-content: space-between; align-items: center; gap: 12px; padding: 6px 0; }
  @media (max-width: 560px) { .packet-invitation { align-items: flex-start; flex-direction: column; } }
</style>
