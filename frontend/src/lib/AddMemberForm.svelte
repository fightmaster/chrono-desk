<script>
  import {createEventDispatcher} from 'svelte'
  import {call} from './api.js'

  export let eventId
  export let races = []
  export let categories = []
  export let defaultRaceId = ''

  const dispatch = createEventDispatcher()

  let open = false
  let error = ''
  let f = blank()

  function blank() {
    return {race_id: defaultRaceId, last_name: '', first_name: '', number: '', epc: '', gender: '', dob: '', category_id: ''}
  }

  $: if (!f.race_id && defaultRaceId) f.race_id = defaultRaceId

  async function submit() {
    error = ''
    if (!f.dob) {
      error = 'Укажите дату рождения'
      return
    }
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
    } catch (e) {
      error = e.message
    }
  }
</script>

{#if open}
  <div class="form">
    <h4>Новый участник</h4>
    {#if error}<p class="error">{error}</p>{/if}
    <div class="grid">
      <label>Фамилия<input bind:value={f.last_name}/></label>
      <label>Имя<input bind:value={f.first_name}/></label>
      <label>Номер<input type="number" bind:value={f.number}/></label>
      <label>Метка (EPC)<input bind:value={f.epc} placeholder="E280…"/></label>
      <label>Пол
        <select bind:value={f.gender}>
          <option value="">—</option>
          <option value="male">М</option>
          <option value="female">Ж</option>
        </select>
      </label>
      <label>Дата рождения<input type="date" bind:value={f.dob}/></label>
      <label>Гонка
        <select bind:value={f.race_id}>
          {#each races as r}<option value={r.id}>{r.name}</option>{/each}
        </select>
      </label>
      <label>Группа
        <select bind:value={f.category_id}>
          <option value="">—</option>
          {#each categories as c}<option value={c.id}>{c.name}</option>{/each}
        </select>
      </label>
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
  .form {
    border: 1px solid #4a5568;
    border-radius: 6px;
    padding: 1rem;
    margin: 0.7rem 0;
    background: #232b38;
  }
  h4 { margin: 0 0 0.5rem; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr)); gap: 0.6rem; }
  label { display: flex; flex-direction: column; gap: 0.2rem; color: #9aa5b1; font-size: 0.85rem; }
  input { background: #1a202c; color: #e2e8f0; border: 1px solid #4a5568; border-radius: 3px; padding: 0.3rem 0.4rem; }
  .actions { margin-top: 0.8rem; display: flex; gap: 0.5rem; }
  .btn { padding: 0.4rem 0.9rem; border-radius: 4px; border: 1px solid #4a5568; background: #2d3748; color: inherit; cursor: pointer; }
  .btn.primary { background: #2b6cb0; border-color: #2b6cb0; }
  .error { color: #e57373; }
</style>
