// Keep one request in flight and at most one pending refresh of the latest state.
export function coalescedTask(run) {
  let active = null
  let queued = false
  let closed = false
  return {
    request() {
      if (closed) return Promise.resolve()
      if (active) { queued = true; return active }
      active = Promise.resolve().then(async () => {
        while (!closed) {
          queued = false
          await run()
          if (!queued) break
        }
      }).finally(() => { active = null })
      return active
    },
    close() { closed = true; queued = false },
  }
}
