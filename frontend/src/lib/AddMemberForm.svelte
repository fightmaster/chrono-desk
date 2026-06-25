<script>
  import {createEventDispatcher} from 'svelte'
  import {call} from './api.js'

  export let eventId
  export let races = []
  export let defaultRaceId = ''

  const dispatch = createEventDispatcher()

  let open = false
  let error = ''
  let f = blank()

  function blank() {
    return {race_id: defaultRaceId, last_name: '', first_name: '', number: '', epc: '', gender: '', dob: '', category_id: ''}
  }
  $: if (!f.race_id && defaultRaceId) f.race_id = defaultRaceId

  // Group options are the categories attached to the selected race (run5's
  // category_race), not the whole catalog.
  let raceCategories = []
  $: loadRaceCategories(f.race_id)
  async function loadRaceCategories(raceId) {
    if (!raceId) { raceCategories = []; return }
    try { raceCategories = await call('GET', `/api/events/${eventId}/races/${raceId}/categories`) }
    catch (_) { raceCategories = [] }
    if (f.category_id && !raceCategories.some(c => c.id === f.category_id)) f = {...f, category_id: ''}
  }

  async function submit() {
    error = ''
    if (!f.dob) { error = 'Укажите дату рождения'; return }
    try {
      const res = await call('POST', `/api/events/${eventId}/members`, JSON.stringify({
        race_id: f.race_id,
        last_name: f.last_name.trim(),
        first_name: f.first_name.trim(),
        number: f.number === '' ? null : Number(f.number),
        epc: f.epc.trim() ? f.epc.trim().toUpperCase() : null,
        gender: f.gender || null,
        dob: f.dob || null,
        category_id: f.category_id || null,
      }))
      f = blank()
      open = false
      dispatch('changed', {recount: res.recount_needed})
    } catch (e) { error = e.message }
  }
</script>

{#if open}
  <div class="card form">
    <div class="ctitle">Новый участник</div>
    {#if error}<p class="error">{error}</p>{/if}
    <div class="grid">
      <div class="field"><span>Фамилия</span><input class="input" bind:value={f.last_name}/></div>
      <div class="field"><span>Имя</span><input class="input" bind:value={f.first_name}/></div>
      <div class="field"><span>Номер</span><input class="input mono" type="number" bind:value={f.number}/></div>
      <div class="field"><span>Метка (EPC)</span><input class="input mono" bind:value={f.epc} placeholder="E280…"/></div>
      <div class="field"><span>Пол</span>
        <select class="input" bind:value={f.gender}>
          <option value="">—</option><option value="male">М</option><option value="female">Ж</option>
        </select></div>
      <div class="field"><span>Дата рождения</span><input class="input mono" type="date" bind:value={f.dob}/></div>
      <div class="field"><span>Гонка</span>
        <select class="input" bind:value={f.race_id}>
          {#each races as r}<option value={r.id}>{r.name}</option>{/each}
        </select></div>
      <div class="field"><span>Группа</span>
        <select class="input" bind:value={f.category_id}>
          <option value="">—</option>
          {#each raceCategories as c}<option value={c.id}>{c.name}</option>{/each}
        </select></div>
    </div>
    <div class="actions">
      <button class="btn primary" on:click={submit}>Добавить</button>
      <button class="btn" on:click={() => { open = false; error = '' }}>Отмена</button>
    </div>
  </div>
{:else}
  <button class="btn" on:click={() => open = true}>+ Участник</button>
{/if}

<style>
  .form { margin-top: 14px; }
  .ctitle { font-size: 16px; font-weight: 700; margin-bottom: 12px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); gap: 13px; }
  .grid .input { width: 100%; }
  .actions { margin-top: 16px; display: flex; gap: 10px; }
</style>
