# Presenter finish snapshots over SSE

Task: CHR-SSE-001
Docs-Impact: LOCAL
Date: 2026-10-24
Stage: implemented in source; synthetic acceptance passed.
Evidence: [synthetic acceptance](../reports/presenter-sse-2026-10-25.md).
Implementation details: [writer/lifecycle inventory](../presenter-sse-internals.md).
The owner authorized commit/push, platform release and delivery to the Mac Desktop;
platform publication/delivery evidence is recorded separately in chrono-docs.
Open scope, priority and acceptance are owned only by `chrono-docs/TODO.md`.
Decision: [ADR-0001](../adr/0001-presenter-sse.md).
Reviewed source baseline: `b027a1e8370c48df01dc23f3f9d34711086140ea`.

## Intended behavior

An active presenter page keeps one SSE connection and receives the current
snapshot of at most ten confirmed finishes when committed finish data changes.
The first change wakes the snapshot worker immediately. There is no 500 ms
debounce, batching timer or mandatory waiting period. While a build or send is
already busy, only the newest pending state is retained. For example, a burst
of thirty finishes can produce one final snapshot containing the latest ten;
all thirty finishes remain stored in the normal timing system.

Club, Team, city, time, gender place and category place remain available. The
existing participation key, ordering, exclusions, ranking policy, scroll anchor
and duplicate suppression remain authoritative. This stream is a current view,
not a durable queue for announcing every athlete exactly once.

Name/affiliation edits need not trigger immediate streaming updates. Add
**Обновить** for a fresh snapshot. Opening Live, returning from Protocol,
changing distance, returning to a visible tab and reconnecting all acquire a
fresh snapshot. Hidden or inactive Live views close their connection and stop
retries. Reconnection history must not highlight old finishes as new arrivals.

## Current boundaries and affected modules

| Boundary | Current implementation | Planned work |
| --- | --- | --- |
| Derived-data commits | `sqlite.Store.WithinTx`, `sqlite.ProcessorRepo.WithTx` and some directly owned transactions | Commit-only, nonblocking invalidation hint |
| RFID capture | `service/live.go`, `service/edge_live.go`, `combinedPublisher` | Preserve processing/ACK boundaries; no HTTP work here |
| Coherent snapshots | `service/live_finishes.go`, `live_finish_ranks.go` | Reuse separate reader pool and shared revision-keyed ranking maps |
| Public LAN transport | `transport/publicweb/server.go`, `live_finishes.go` | Bounded broadcast hub and additive GET stream |
| Presenter lifecycle | `transport/publicweb/live.js` | SSE lifecycle, explicit refresh, old-server polling compatibility |
| Tests | Adjacent Go tests and `frontend/scripts/presenter-smoke.mjs` | Two-client, real-ingest, backpressure and resource-lifecycle evidence |

Native input currently commits owned raw data before processing derived state.
Edge/sidecar input wraps admission and processing in an outer `Store.WithinTx`;
the processor joins it. Consequently, notifying after a nested processor call
returns can expose data which subsequently rolls back. Notify only after the
outermost successful projection commit. The existing native raw/projection
recovery issue remains owned by CHR-DESK-REC-001.

This is a Desk-local read API. Observation identity, raw ownership, timing
rules, schema, export, site sync and shared `timing-core` contracts are unchanged.
No shared registry version or coordinated RUN5/sidecar release is required.

## Commit notification and scheduling

1. Audit every application writer which can create or change eligible finishes:
   native and combined input, sidecar input, manual finish, targeted/full recount,
   corrections, pull/import and event replacement. Record the actual outer commit
   boundary in tests. Direct SQL in a fixture is not evidence for a real writer.
2. Introduce a small infrastructure-neutral notification contract, wired in the
   composition layer. Infrastructure/processor code must not import `publicweb`.
   An outer transaction collects a possible-finish-change flag; nested adapters
   share it. After successful commit, advance an event-local in-memory generation
   and try to signal a capacity-one wake channel. No SQL, ranking, encoding,
   subscriber iteration, socket write or goroutine creation in this callback.
3. Rollback and commit failure emit nothing. Raw-only ingestion, duplicate reads
   and intermediate checkpoints which do not change an eligible finish must not
   cause repeated snapshot builds. Finishing/rank-relevant derived changes can
   conservatively invalidate; metadata-only changes may wait for refresh.
4. One dispatcher per actively broadcast event consumes hints and wakes active
   view groups. Each group is keyed by publication epoch and validated distance
   filter, and has at most one snapshot build running. Clients sharing a group
   share its immutable encoded snapshot and existing ranking cache.
5. Register interest before the initial read. Sample the generation before
   starting a read snapshot, build once, release all rows/transactions/leases,
   then compare generation again. A newer generation schedules another immediate
   pass. A channel being full must never consume or lose the only notification.
   Under continuous arrivals, publish completed snapshots and continue; do not
   wait for a permanently quiet generation before sending anything.
6. Suppress identical public snapshots. Raw traffic and heartbeat ticks never
   initiate DB reads. Forced refresh always obtains committed current data even
   when the last finish generation has not changed.

The existing reader pool reduces writer contention; it does not make snapshot
queries cost-free. Keep build concurrency bounded by the reader pool, give builds
a one-second context deadline, cancel obsolete groups, and benchmark while ingest
is running. Do not hold any hub lock during queries, encoding or network writes.

## LAN API and compatibility

Keep `GET /api/live/finishes?race_id=...`. Add
`GET /api/live/finishes/stream?race_id=...` on the same operator-enabled,
read-only LAN listener. HTTP/1.1 is sufficient; retain the current protocol and
Mac-compatible Go toolchain. No new streaming dependency is needed.

The JSON snapshot may advertise additive `stream_version: 1`. A new browser
first acquires a fresh JSON snapshot, then opens SSE when advertised. Against an
older server, absence of the capability keeps the existing visible-only poller.
Older browsers using the JSON endpoint continue to work. SSE errors on a server
advertising support trigger reconnect, not a simultaneous polling loop.

The stream uses UTF-8 `text/event-stream`, `Cache-Control: no-store`, immediate
flush of complete frames, and no compression/buffering middleware. Validate
method, filter, publication and admission budget before sending stream headers.
The snapshot payload uses the existing privacy-trimmed DTO and at most ten rows:

```text
retry: 1000
event: snapshot
id: <opaque publication-epoch/view-revision token>
data: {"published":true,"rows":[...],...}

event: heartbeat
data: {}

```

The example payload is schematic. Production frames contain valid JSON with the
complete existing DTO. Heartbeats carry no snapshot ID and do not change cards.
Proposed heartbeat period: five seconds; stale-connection watchdog: fifteen
seconds. Heartbeat receipt proves transport liveness, not that a newer finish
exists. UI must distinguish connection health from last finish/data update.

Every connection/reconnection gets a newly read snapshot, including reconnects
with stale, unknown or previous-process `Last-Event-ID`. Tokens identify view
revisions, not a wall-clock cursor or an RFID offset. Do not compare opaque tokens
lexicographically. Publication changes cancel the old epoch; revalidate that
epoch before enqueue and send so old-event frames cannot populate a new event.
Unpublish cancels streams and closes the listener; reconnect uses the existing
unavailable indication. An invalid distance returns 400; unavailable snapshot
returns 503 before streaming. Later build errors close the stream so reconnect
can retry; never send a success heartbeat indefinitely after a failed build.

Keep public fields unchanged: no raw member IDs, EPCs, DOB, contacts, credentials
or new event access. Set an initial maximum of 32 active SSE clients; reject
additional streams with 503 and retry guidance before allocating workers. The
ordinary JSON endpoint remains available. Document/tune this resource budget
from evidence rather than claiming unbounded client support.

## Backpressure and resource ownership

Each client owns one HTTP handler and one latest-pending immutable snapshot
reference, plus any frame currently being written. Replacing pending state is
bounded and never waits for that client's socket. Fan-out runs outside the RFID
path. Closing a client must not close a channel another publisher can still send
to; use explicit ownership and cancellation rather than send/close races.

Set a fresh write deadline before every frame write and flush using
`http.ResponseController` (initial budget: one second). An expired deadline or
write/flush error terminates that client. Reset deadlines for later writes;
do not apply a short absolute `http.Server.WriteTimeout` to a long-lived stream.
Test with both a controllably blocked writer and a real TCP peer whose receive
buffer eventually fills; a small snapshot may fit in kernel buffers and conceal
backpressure in an ordinary two-client smoke test.

The request context, publication epoch and application context all cancel the
handler. Unsubscribe releases its pending frame, heartbeat/watchdog timers and
view reference. Drop unused group caches/workers when the last subscriber leaves;
remove event subscriptions on store close/replacement, unpublish and shutdown.
Repeated publish/switch/stop must not accumulate observers on a cached store.

Cancel hub workers/handlers before `http.Server.Shutdown`: it waits for active
connections to become idle. Wait for owned workers within the shutdown budget;
force-close remaining connections on deadline and report failure. Never wait
under the server mutex. None of this may stop or await the RFID ingestion loop.

The memory bound is proportional to admitted clients and active validated views,
not to elapsed race time or finish count. Keep one cached frame per group and
one pending/in-flight reference per admitted client; do not retain historical
frames, per-finish goroutines or per-connection database transactions.

## Browser lifecycle

Extend `createFinishFeed` rather than create a second live renderer. A single
generation owns either polling, an initial/forced JSON request, or one active
EventSource. Close/invalidate the previous generation on filter change, Refresh,
hidden tab, Protocol navigation and stop; ignore stale callbacks. Avoid manual
retry timers competing with EventSource's built-in reconnect. A watchdog may
close and replace a stalled EventSource, but must first cancel the old one.

On Refresh or view reactivation, invalidate the old stream, fetch fresh data and
then open a stream which also supplies its own initial snapshot. Thus a finish
between the GET and subscription is recovered. Snapshot revisions/generation
guards prevent an older GET response from replacing a newer streamed snapshot.
Preserve last good rows during an outage and show a clear stale-data warning.

## Test design and evidence

Use synthetic participants, temporary SQLite files and loopback devices only.
Concurrency tests should use barriers/channels and bounded deadlines rather
than fragile sleeps. New tests must be exercised under `go test -race`.

| Test layer | Required assertions |
| --- | --- |
| Commit observer | Outer commit only; no notification before commit, on rollback/failure, or from a joined nested transaction. Native and sidecar paths both covered. Raw commit followed by projection failure never announces an uncommitted finish. |
| Notification races | Finish between subscribe/initial read, during snapshot build and during enqueue is eventually included. Capacity-one signals cannot lose the newest generation. First finish wakes immediately without a batching clock. |
| Two real HTTP clients | Both receive initial data and multiple committed updates. Closing A leaves B working; A reconnects to a fresh snapshot. Race filters, event switch and old IDs behave correctly; no duplicates or more than ten rows. Burst thirty yields the latest ten. |
| Slow client | Two normal clients continue while a third writer is blocked. Assert producer processing/sidecar ACK completes before releasing the blocked writer. Pending state stays bounded; deadline/cancellation removes only the slow subscriber. |
| Real processing integration | Feed native and sidecar/combined fixtures through their actual publisher/processor/commit paths while clients connect, disconnect and receive updates. Raw ownership/outbox, duplicate handling, derived outcomes and ACK counts equal the non-SSE baseline. Include an ordinary judge edit/recount alongside ingestion. |
| Idle behavior | Two idle healthy streams perform no repeated snapshot queries, encoding or card reconciliation. Tiny heartbeats continue; 100 raw/intermediate/duplicate reads do not generate finish snapshots. |
| Snapshot correctness | Existing opaque keys, Club/Team, gun/chip ranking, category/gender places, exclusions and ordering remain. Manual refresh and return from Protocol recover metadata/corrections/revocations. Queries/rows/transactions are released before a socket blocks. |
| Browser | Two independent pages: live delivery, hidden/inactive close, one transport per page, filter cancellation, offline/online and sleep/reconnect, stale callback rejection, scroll/highlight behavior, force refresh, old-server fallback. No full-protocol requests. |
| Shutdown | Cancel, unpublish, switch event, store replacement and repeated publish/unpublish terminate handlers/workers and permit rebinding. No send-on-closed-channel, deadlock or retained old-event subscription. |

### Leak regression

Warm the fixture/caches first and keep its participant/observation counts fixed.
Then run at least 1,000 connect/read/cancel/reconnect cycles in several batches,
including cancellation during initial build, blocked send and heartbeat. Close
test HTTP clients' idle connections before measuring their socket lifetimes.

Assert owned active-subscription, worker and timer counts return to baseline;
all owned wait groups drain, DB reader `InUse` returns to baseline, and cancelled
groups/maps retain no clients. Inspect goroutine profiles and file-descriptor
counts around the loop. Absolute process goroutine counts alone are too noisy
to prove ownership or cleanup.

For a controlled leak run, capture heap and goroutine profiles after warm-up and
after each batch, forcing GC in the test harness only. Compare retained objects
and `HeapAlloc` against a no-stream control and inspect allocation stacks for a
repeatable per-cycle slope. Freeze a numeric retained-heap tolerance from that
baseline before using it as a gate; do not invent a passing threshold afterward.
RSS alone is insufficient because Go may retain reusable memory. This provides
regression evidence; a finite run cannot prove the absence of every memory leak.
Keep profiling local/test-only and avoid a public `pprof` route.

### Load and latency

Run matched SSE-off/on scenarios with two normal clients plus a slow/flapping
client, 100 synthetic observations per second, one finish per second and bursts.
Measure at least 100 updates per normal browser client. Record capture-to-commit
separately from commit-to-DOM plus two animation frames. Proposed LAN-target
p95 for the latter is <=250 ms; retain the existing <=1,000 ms browser regression
guard until the new target is verified on a controlled runner. No fixed batching
delay is acceptable even if a broad latency guard passes.

Repeat matched ingest benchmarks at least three times. Report throughput, commit
and ACK latency, allocations, snapshot builds, query counts and maximum queue
depth. Target no more than 10% p95 ingest regression outside measured runner noise;
record baseline/noise before implementation. Exact input/output counts, no lost
ACKs and deterministic blocked-client isolation are mandatory regardless of
timing noise. Add a 30-minute opt-in synthetic soak to check heap plateau,
goroutine/FD stability and idle periods. Keep fast lifecycle tests in ordinary CI.

## Implementation sequence and release

1. Add failing commit-boundary and two-client/backpressure tests. Inventory every
   relevant writer; choose the smallest shared transaction-hook extraction.
2. Implement post-commit hints and bounded snapshot worker ownership. Prove
   rollback, lost-wakeup, real-ingest and idle-query invariants before HTTP wiring.
3. Add the GET stream, deadline/heartbeat behavior, admission and shutdown cleanup.
   Extend real HTTP tests and repeat lifecycle/leak tests under the race detector.
4. Switch the existing browser feed through capability detection; add Refresh and
   lifecycle tests. Preserve old JSON clients and server compatibility.
5. Run the full gate (`make quality`), `govulncheck ./...`, `go build ./...`, the
   opt-in browser test and the controlled soak/profiles. Record exact source,
   commands, hardware, metrics and any failures. Existing Go 1.24 security findings
   remain CHR-SEC-002; do not claim a clean vulnerability audit or silently upgrade
   beyond the competition Mac's supported toolchain.
6. Publish only with explicit release scope. Check the packaged Mac app and two
   tablet browser clients when available; distinguish synthetic evidence from
   actual Wi-Fi measurements. Roll back by restoring the prior app/client assets;
   no schema/data rollback is needed. Update implementation docs and central
   current state; close CHR-SSE-001 only when its acceptance is evidenced.

## References

- [Existing presenter behavior](../presenter-live.md)
- [Snapshot/read-pool boundaries](../snapshot-recount.md)
- [WHATWG SSE framing and EventSource lifecycle](https://html.spec.whatwg.org/multipage/server-sent-events.html)
- [Go ResponseController](https://pkg.go.dev/net/http@go1.24.13#ResponseController)
- [Go server shutdown semantics](https://pkg.go.dev/net/http@go1.24.13#Server.Shutdown)
