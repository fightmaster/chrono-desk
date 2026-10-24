# ADR-0001: Push bounded presenter snapshots over SSE

Date: 2026-10-24
Task: CHR-SSE-001
Docs-Impact: LOCAL
Decision stage: implemented in source; acceptance evidence and deployment are separate.
Open work and acceptance: `chrono-docs/TODO.md`.

## Context

Before this change the LAN presenter polled the latest ten finishes every 500 ms. Finish
arrivals vary from continuous bursts to long idle periods. The presenter and a
second judge need low latency while the main judge operates the desktop. Two or
three clients are expected; clients may sleep, disconnect or stop reading.

## Decision

Use one standard EventSource/SSE connection per active presenter view. Send the
current bounded snapshot after committed finish changes and on subscription.
Keep the existing read-only JSON endpoint for refresh and older clients. HTTP/1.1
on the existing operator-enabled LAN listener is sufficient.

Trigger the first build immediately after the outer projection transaction
commits. Coalesce only work already pending while a build/send is busy; there is
no periodic batching delay. Writer-side work is a nonblocking generation/hint
update. Snapshot reads, ranking, JSON encoding and socket writes run separately.
Share immutable snapshots per active event/filter, and give each admitted client
one replaceable pending snapshot. Slow clients expire through write deadlines.

Reconnect always reloads the latest snapshot. This has current-view semantics,
not durable delivery/replay of each individual finish. Opaque publication/view
tokens are independent of observation identity, timestamps and sync cursors.
Metadata can refresh through the next snapshot or an explicit Refresh action.
Hidden/inactive views release their stream. Cancellation and worker ownership
must cover disconnect, unpublish, event replacement and application shutdown.

## Alternatives considered

- Timestamp/ETag polling saves response bodies but keeps empty requests and an
  interval-based latency floor. It remains compatibility behavior for old servers.
- Long polling also works, but requires repeated request lifecycles and timeout
  coordination. Native EventSource provides the required one-way transport.
- WebSocket/gRPC add transport and client machinery without a bidirectional or
  binary-stream requirement. No HTTP/2 migration is necessary for this local API.
- Per-client DB queries and queues of every finish multiply work and retention.
  The product asks for the latest ten, so view-sharing and latest-only pending
  state fit its semantics.

## Consequences and acceptance

SSE retains active HTTP handlers/connections; admission, deadlines and explicit
cleanup are required. HTTP shutdown alone does not cancel active streams. A
wrong commit hook could expose rolled-back data or miss a writer path; tests must
exercise native, sidecar and correction transactions rather than only direct SQL.
Read-pool isolation still needs a concurrent ingestion benchmark.

Two-client updates, blocked-client isolation, race detection, repeated lifecycle
tests, retained-heap/goroutine profiles and a bounded soak are specified in the
[implementation and test plan](../plans/presenter-sse.md). They are required
evidence before claiming production readiness. Shared timing, export, sync and
raw-observation contracts remain unchanged.
