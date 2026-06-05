<script>
  import {onMount} from 'svelte'
  import {APIBaseURL} from '../wailsjs/go/main/App.js'

  let apiBase = ''
  let apiStatus = 'подключение…'

  onMount(async () => {
    try {
      apiBase = await APIBaseURL()
      const resp = await fetch(`${apiBase}/health`)
      const body = await resp.json()
      apiStatus = body.status === 'ok' ? 'онлайн' : `ошибка: ${resp.status}`
    } catch (err) {
      apiStatus = `недоступен (${err})`
    }
  })
</script>

<main>
  <h1>Chrono Desk</h1>
  <p class="subtitle">Оффлайн-обработка результатов соревнований</p>
  <p class="status">API: <span class:ok={apiStatus === 'онлайн'}>{apiStatus}</span></p>
</main>

<style>
  main {
    padding: 4rem 2rem;
    text-align: center;
  }

  .subtitle {
    color: #9aa5b1;
  }

  .status {
    margin-top: 2rem;
    font-family: monospace;
  }

  .status .ok {
    color: #4caf50;
  }
</style>
