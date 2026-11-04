// One server request commits one bounded, durably acknowledged batch.
// A failure leaves prior acknowledgements intact and is surfaced to the caller.
export async function drainSync(send, progress = () => {}, maxBatches = 100) {
  let batches = 0
  let observations = 0
  for (;;) {
    const result = await send()
    batches++
    observations += result.sent?.rfid_logs ?? 0
    progress({ batches, observations, remaining: result.remaining_observations ?? 0 })
    if (!result.has_more || batches >= maxBatches) return { ...result, sent: { ...result.sent, rfid_logs: observations }, batches, observations }
    if (!(result.sent?.rfid_logs > 0)) throw new Error('Очередь не продвигается; повторите отправку позже.')
  }
}
