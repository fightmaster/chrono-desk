<script>
  import {createEventDispatcher, onMount} from 'svelte'
  import {call} from './api.js'
  import EdgeRelay from './EdgeRelay.svelte'

  export let eventId
  export let status = {}
  export let ips = []
  const dispatch = createEventDispatcher()
  let combined = status.running ? !!status.combined : true
  let port = status.port || '5084'
  let bindings = []
  let pending = 0
  let error = ''
  let busy = false
  let loaded = false
  let savedBindings = '[]'
  $: bindingsDirty = loaded && JSON.stringify(bindings) !== savedBindings

  async function load() {
    const config = await call('GET', `/api/events/${eventId}/live/edge/config`)
    bindings = config.bindings
    savedBindings = JSON.stringify(bindings)
    pending = config.relay_pending
    loaded = true
  }
  onMount(() => { load().catch(e => error = e.message) })

  async function act(operation) {
    error = ''; busy = true
    try {
      if (operation === 'save') {
        await call('PUT', `/api/events/${eventId}/live/edge/config`, JSON.stringify({bindings}))
      } else {
        const current = await call('POST', `/api/events/${eventId}/live/edge/${operation}`, JSON.stringify({port, combined}))
        dispatch('status', current)
      }
      await load()
    } catch (e) { error = e.message }
    finally { busy = false }
  }
</script>

<details class="edge">
  <summary>Feibot + RFID Edge / plate {status.running ? '· приём включён' : ''}</summary>
  <p>Совместный вход принимает штатный Feibot и Edge на одном адресе. Перед его запуском остановите прежний отдельный вход. Только доверенная локальная сеть: не открывайте этот порт в интернет.</p>
  <p>Адрес Desk в настройках Feibot: <strong>{ips[0] || 'IP компьютера'}:{status.port || port}</strong>. Edge с включённым следованием настройкам Feibot использует этот же адрес. Чекпоинты нужны для расчёта, но не для сохранения сырых отметок; один прибор может обслуживать несколько точек.</p>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if status.last_error}<p class="error">Последняя ошибка приёма: {status.last_error}</p>{/if}
  <fieldset disabled={busy || status.running || !loaded}>
    {#each bindings as binding, i}
      <div class="binding">
        <label>Board<input class="input mono" bind:value={binding.board} maxlength="128" /></label>
        {#if binding.board.startsWith('Feibot:') && (!binding.source_session_id || binding.source_session_id === eventId)}
          <label>Сессия Feibot<input class="input mono" value={eventId} readonly /><small>Из выбранного события; копировать из sidecar не нужно.</small></label>
        {:else}
          <label>Сессия plate / явная историческая сессия<input class="input mono" bind:value={binding.source_session_id} maxlength="96" /></label>
        {/if}
        <button class="btn" on:click={() => bindings = bindings.filter((_, index) => index !== i)}>Убрать</button>
      </div>
    {/each}
    <button class="btn" disabled={bindings.length >= 64} on:click={() => bindings = [...bindings, {board: '', source_session_id: ''}]}>Добавить прибор</button>
    <button class="btn" disabled={bindings.length >= 64} on:click={() => bindings = [...bindings, {board: 'Feibot:', source_session_id: ''}]}>Добавить Feibot</button>
    <button class="btn" on:click={() => act('save')}>Сохранить привязки</button>
    <label>TCP-порт<input class="input mono" bind:value={port} inputmode="numeric" /></label>
    <label><input type="checkbox" bind:checked={combined} /> Принимать штатный Feibot на этом же порту</label>
  </fieldset>
  {#if bindingsDirty}<p role="status">Перед запуском сохраните изменённые привязки.</p>{/if}
  {#if status.running}
    <button class="btn" disabled={busy} on:click={() => act('stop')}>Остановить edge-вход</button>
  {:else}
    <button class="btn primary" disabled={busy || !loaded || bindingsDirty || bindings.length === 0} on:click={() => act('start')}>Запустить edge-вход</button>
  {/if}
  <p>Принято сообщений: {status.received || 0} · новых: {status.inserted || 0} · повторов: {status.duplicates || 0} · ошибок сохранения: {status.errors || 0}.</p>
  <p>В отдельном журнале ожидают пересылки: {pending}. Исходные пакеты сохраняют своё происхождение и не преобразуются в прежний формат Desk.</p>
  <button class="btn" disabled={busy} on:click={() => load().catch(e => error = e.message)}>Обновить настройки и очередь</button>
  <EdgeRelay {eventId} />
</details>

<style>
  .edge{border:1px solid var(--border, #777);border-radius:8px;padding:12px;margin:12px 0}
  summary{cursor:pointer;font-weight:600}fieldset{border:0;padding:0;margin-bottom:12px}
  .binding{display:flex;flex-wrap:wrap;gap:8px;align-items:end;margin:12px 0}
  label{display:block;margin:8px 0}input{display:block;max-width:100%}.binding label{flex:1;min-width:160px}
</style>
