// A drawer may be closed or reloaded before an earlier response resolves.
export function latestRequest() {
  let active = null
  return {
    begin() {
      active?.abort()
      const request = new AbortController()
      active = request
      return {signal: request.signal, current: () => active === request && !request.signal.aborted}
    },
    cancel() {
      active?.abort()
      active = null
    },
  }
}
