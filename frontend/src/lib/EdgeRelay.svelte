<script>
  import {onMount} from 'svelte'
  import {call} from './api.js'

  export let eventId
  let endpoint = ''
  let enabled = false
  let revision = 0
  let savedEndpoint = ''
  let tlsBundle = ''
  let savedTLSBundle = ''
  let confirmPending = false
  let loaded = false
  let busy = false
  let error = ''
  let running = false
  let lastError = ''
  let progress = {pending: 0, acked: 0, attempts: 0}
  $: redirectPending = progress.pending > 0 && (endpoint.trim().replace(/^tcp:\/\//, '') !== savedEndpoint || tlsBundle !== savedTLSBundle)
  $: secure = endpoint.trim().startsWith('tls://')

  function apply(value) {
    endpoint = value.config.endpoint
    savedEndpoint = endpoint
    tlsBundle = value.config.tls_bundle || ''
    savedTLSBundle = tlsBundle
    enabled = value.config.enabled
    revision = value.config.revision
    progress = value.progress
    running = value.running
    lastError = value.last_error || progress.last_error || ''
    confirmPending = false
    loaded = true
  }
  async function refresh() {
    error = ''; busy = true
    try { apply(await call('GET', `/api/events/${eventId}/live/edge/relay`)) }
    catch (e) { error = e.message }
    finally { busy = false }
  }
  async function save() {
    error = ''; busy = true
    try {
      apply(await call('PUT', `/api/events/${eventId}/live/edge/relay`, JSON.stringify({endpoint, enabled, revision, tls_bundle: tlsBundle, confirm_pending: confirmPending})))
    } catch (e) { error = e.message }
    finally { busy = false }
  }
  onMount(() => { refresh() })
</script>

<section aria-label="Досылка sidecar в Hub">
  <h3>Досылка исходных пакетов в Hub</h3>
  <p>Работает независимо от входов и открытого экрана. После запуска Desk включённая досылка возобновляется автоматически. Адрес должен вести на подготовленный edge-вход Hub, не на нативный Feibot-порт.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if lastError}<p class="error">Последняя ошибка досылки: {lastError}</p>{/if}
  <fieldset disabled={busy || !loaded}>
    <label>Адрес edge-входа Hub<input class="input mono" bind:value={endpoint} placeholder="host:port" maxlength="270" /></label>
    {#if secure || tlsBundle}
      <label>Каталог отдельного сертификата Desk<input class="input mono" bind:value={tlsBundle} maxlength="4096" /></label>
      <p>Для tls:// нужны client.pem, client-key.pem и server-ca.pem в закрытом каталоге этого компьютера. Не копируйте ключ Feibot. Первичную выдачу сертификата и разрешения Hub выполняет администратор.</p>
    {/if}
    <label><input type="checkbox" bind:checked={enabled} /> Автоматически досылать в Hub</label>
    {#if redirectPending}
      <label><input type="checkbox" bind:checked={confirmPending} /> Подтверждаю досылку сохранённой очереди ({progress.pending}) на указанный адрес. Событие, сессия и исходные пакеты не изменятся.</label>
    {/if}
    <button class="btn" disabled={(enabled && !endpoint.trim()) || (redirectPending && !confirmPending)} on:click={save}>Сохранить досылку</button>
  </fieldset>
  <p role="status">Отправитель: {running ? 'включён' : 'остановлен'} · ожидают: {progress.pending} · подтверждены Hub: {progress.acked} · попыток: {progress.attempts}.</p>
  <p>Подтверждение Hub означает сохранение в его очереди, а не готовый результат на сайте. Привязка события и сессии должна быть разрешена на Hub и сервере. Отключение досылки не удаляет очередь.</p>
  <button class="btn" disabled={busy} on:click={refresh}>Обновить состояние досылки</button>
</section>

<style>
  section{border-top:1px solid var(--border, #777);margin-top:16px;padding-top:8px}
  fieldset{border:0;padding:0}label{display:block;margin:8px 0}.input{display:block;max-width:100%}
</style>
