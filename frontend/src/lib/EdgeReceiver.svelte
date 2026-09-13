<script>
  import {onMount} from 'svelte'
  import {call} from './api.js'
  import EdgeRelay from './EdgeRelay.svelte'

  export let eventId
  export let status = {}
  let bindings = []
  let pending = 0
  let error = ''
  let busy = false
  let loaded = false
  let savedBindings = '[]'
  let automaticFeibot = true
  $: bindingsDirty = loaded && JSON.stringify(bindings) !== savedBindings

  async function load() {
    const config = await call('GET', `/api/events/${eventId}/live/edge/config`)
    bindings = config.bindings
    savedBindings = JSON.stringify(bindings)
    pending = config.relay_pending
    automaticFeibot = config.automatic_feibot !== false
    loaded = true
  }
  onMount(() => { load().catch(e => error = e.message) })

  async function save() {
    error = ''; busy = true
    try {
      await call('PUT', `/api/events/${eventId}/live/edge/config`, JSON.stringify({bindings}))
      await load()
    } catch (e) { error = e.message }
    finally { busy = false }
  }
</script>

<section class="edge-status" aria-label="Приём Feibot и RFID Edge">
  <p>Feibot и RFID Edge принимаются обычной кнопкой «Запустить приём» на штатном порту. Выберите в Feibot то же событие, что открыто в Desk, и укажите штатный адрес Desk. Добавлять прибор, копировать сессию или сохранять Edge-настройки не нужно.</p>
  {#if loaded && !automaticFeibot}<p class="error" role="status">Для этого события ранее задан явный список источников. Он сохранён, включая запрет удалённых источников; автоматический приём новых Feibot ограничен этим списком. Изменение ограничений доступно только в расширенных настройках ниже.</p>{/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if status.last_error}<p class="error" role="alert">Последняя ошибка приёма: {status.last_error}</p>{/if}
</section>
<details class="edge">
  <summary>Расширенные настройки: plate, исторические источники и резервная досылка</summary>
  <p>Обычный Feibot с RFID Edge не нужно добавлять здесь: кнопка «Запустить приём» принимает оба формата на штатном порту. ID события и адрес Desk задаются только в Feibot; отметки другого события не принимаются. Чекпоинт для сохранения сырых отметок не требуется.</p>
  <p>Сохранение списка ниже включает явное ограничение Edge-источников, в том числе Feibot. Это экспертная настройка для plate, исторических сессий и отзыва доступа, а не шаг подключения обычного Feibot.</p>
  <fieldset disabled={busy || status.running || !loaded}>
    {#each bindings as binding, i}
      <div class="binding">
        <label>Board<input class="input mono" bind:value={binding.board} maxlength="128" /></label>
        {#if binding.board.startsWith('Feibot:') && (!binding.source_session_id || binding.source_session_id === eventId)}
          <small>Сессия Feibot: событие {eventId}; вручную вводить её не нужно.</small>
        {:else}
          <label>Сессия plate / явная историческая сессия<input class="input mono" bind:value={binding.source_session_id} maxlength="96" /></label>
        {/if}
        <button class="btn" on:click={() => bindings = bindings.filter((_, index) => index !== i)}>Убрать</button>
      </div>
    {/each}
    <button class="btn" disabled={bindings.length >= 64} on:click={() => bindings = [...bindings, {board: '', source_session_id: ''}]}>Добавить явный источник</button>
    <button class="btn" on:click={save}>Сохранить источники</button>
  </fieldset>
  {#if bindingsDirty}<p role="status">Сохраните источники, затем запустите общий приём обычной кнопкой сверху.</p>{/if}
  <p>Принято сообщений: {status.received || 0} · новых: {status.inserted || 0} · повторов: {status.duplicates || 0} · ошибок сохранения: {status.errors || 0}.</p>
  <p>В отдельном журнале ожидают пересылки: {pending}. Исходные пакеты сохраняют своё происхождение и не преобразуются в прежний формат Desk.</p>
  <button class="btn" disabled={busy} on:click={() => load().catch(e => error = e.message)}>Обновить настройки и очередь</button>
  <details class="advanced">
    <summary>Расширенные настройки: резервная досылка в Hub</summary>
    <p>Нужны только когда источник не отправляет данные в Hub самостоятельно. Обычный RFID Edge ведёт собственную независимую очередь на сайт.</p>
    <EdgeRelay {eventId} />
  </details>
</details>

<style>
  .edge{border:1px solid var(--border, #777);border-radius:8px;padding:12px;margin:12px 0}
  .advanced{margin-top:16px}
  summary{cursor:pointer;font-weight:600}fieldset{border:0;padding:0;margin-bottom:12px}
  .binding{display:flex;flex-wrap:wrap;gap:8px;align-items:end;margin:12px 0}
  label{display:block;margin:8px 0}input{display:block;max-width:100%}.binding label{flex:1;min-width:160px}
</style>
