# Bounded site delivery

Task: CHR-SYNC-001. Docs-Impact: LOCAL. Native sync v3 wire is unchanged.

New owned observation batches use1,000 rows rather than20,000. A previously
sealed sent batch is retried with its original ID/range/content even if it is
larger. The per-event observation journal is the sole raw outbound source;
imported site/Edge observations do not become Desk-owned.

The internal sync response adds remaining_observations and has_more. A manual
Send action drains sequential batches, preserving each durable ACK before the
next request. Completed counts accumulate in the UI. Failure preserves earlier
ACKs and leaves the current sealed batch available for retry. A manual action
stops after100batches if new live intake prevents the queue from emptying; the
UI reports the remaining count and allows continuation. The event and overwrite
choice are captured at the start of the action.

The authenticated sync HTTP transport retries429 responses according to
Retry-After (seconds or HTTP date,60s fallback), up to three retries within the
existing120s client timeout/context. Each retry reopens the same request body.
Other status codes, transport failures and ambiguous ACKs retain the existing
explicit durable retry behavior. Redirect denial and HTTP/1.1-only TLS policy
remain enforced. CloseIdleConnections forwards to the underlying transport.

Regression coverage: sync_batching_test.go sends10,001 owned observations in
11bounded requests and verifies durable ACKs; existing imported rows do not
enter that stream. drain-sync.test.mjs covers12,909rows, interrupted delivery,
old internal responses, stalled queues and continuously arriving data.
sync_retry_test.go covers exact-body replay, Retry-After and cancellation.

Event194712 forensic evidence lives in RUN5's
/docs/reports/archeda-194712-delivery-2026-11-05.md. Installed appliances/Desk are
not changed merely by preparing this source. New native artifacts require the
normal release, signature and field validation workflow.
