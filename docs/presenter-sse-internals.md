# Presenter SSE commit and lifecycle boundaries

Task: CHR-SSE-001. Docs-Impact: LOCAL.
Open work and acceptance belong to `chrono-docs/TODO.md`.

## Writer inventory

The notification denotes a possible change to a committed view, not a new raw
observation. Public payload comparison suppresses unchanged snapshots. A
transaction-bound Store holds one dirty flag shared by joined calls. Only its
outer successful commit advances the store-lifetime `finishchange.Signal`.
Standalone statements mark only after their successful autocommit.

| Path | Actual commit / notification boundary | Evidence |
| --- | --- | --- |
| Native RFID | Raw ownership insert commits first; `ProcessorRepo.WithTx` commits projection separately and then notifies only changed finish/member timing | `TestFinishStreamRealNativeAndSidecarACKWhileWriterBlocked`, `TestFinishStreamFailedProjectionNeverAnnouncesRawCommit` |
| Sidecar / combined input | `edgePublisher.Publish` owns `Store.WithinTx`; joined processor changes share its flag; notify after admission plus projection commit, before ordinary ACK | Same real TCP test, nested processor commit/rollback test |
| Manual finish | `service/manual.go` outer `WithinTx` includes result, journal and `UpdateMemberFinish`; notification follows outer commit | `TestFinishChangesFollowOutermostCommit`, existing manual result/recount tests |
| Full / targeted recount | `Recounter.Recount` / `RecountPlan` owns `WithinTx`; wipe/snapshot materialization/member finalization share its flag | Real TCP test includes ordinary edit/recount; existing scoped/full snapshot/fence/rollback tests |
| Site change pull | `SyncPullManager` executes the revision-checked recount plan through the same transaction adapter | Existing site-pull/planner tests and the common recount transaction hook |
| Event import | `ApplyEventImport` owns an explicit root transaction and notifies after its Commit; both ordinary member upsert and packet-scope timing update are covered | Existing import atomicity tests plus shared hook/source inventory |
| Member upsert / judge edit | `UpsertMember`, `UpdateMemberFinish`, whitelisted member/race/category field edits and assigned-start shifts mark the current outer transaction; standalone autocommit variants mark after success | Commit/rollback tests and existing edit/start/ranking tests |
| Packet registration update | `PutPacketRegistration` marks status/race/category/member changes; caller owns the packet transition transaction | Existing packet transition/site-feed/owner-fence tests |
| Registration-only creation, roster setup and deletion | Packet metadata operations do not create a confirmed finish; the next committed finish, view activation or explicit Refresh obtains their current state | Existing eligibility/registration tests; shared fresh JSON/stream snapshot path |
| Club-only edits/refresh | `member_club_metadata` changes deliberately wait for next snapshot/Refresh; no timing replay/rank invalidation | Browser force-refresh test and existing Club/fence tests |
| Raw/history imports, ownership journals, duplicates and intermediate passages | No finish hint from raw commit alone; processor marks only an actual finish or start change of an already finished member | Real mixed TCP test sends100 unmatched reads and duplicate frames; intermediate/rollback unit tests |

No application write bypasses infrastructure through `Store.DB().Exec`:
production DB access outside infrastructure is only the existing rank-cache
identity key. Direct SQL fixture writes must not be mistaken for coverage of a
real writer. Browser fixture changes now use Store methods; actual native/Edge
tests additionally exercise decoding, admission, projection, ownership and ACK.

Failed deferred-FK COMMIT injection exposed the existing driver/transaction
cleanup issue tracked separately as CHR-DESK-TX-001. No finish notification is
emitted on that failure. Its subsequent connection recovery is not claimed fixed
by this transport change. Native raw/projection crash recovery likewise remains
CHR-DESK-REC-001; SSE never pretends a durable raw row is a confirmed finish.

## Resource ownership

```text
outer projection COMMIT
  -> atomic generation + capacity-one wake (no callbacks or goroutine creation)
  -> one dispatcher per published event/store epoch
  -> bounded view workers (one per active distance, at most four builds)
  -> one shared immutable snapshot per view
  -> one replaceable pending frame per client
  -> that client's HTTP handler / write deadline
```

The hub has no socket writes and no DB work under its mutex. Builds use a separate
read snapshot and release its lease before JSON encoding or fan-out. A generation
sample before reading and a post-build comparison prevent a commit during a read
from being lost. Completed snapshots are published even under continuous input.
A joining subscriber waits for a build started after it subscribed, so it cannot
inherit a snapshot already pinned before that connection. Identical payloads
reuse the frame; existing subscribers skip an unchanged frame ID.

The single store-signal dispatcher is stopped before another hub can consume its
wake channel. Publication epochs and process-keyed opaque IDs isolate event
switches/restarts. Unknown `Last-Event-ID` does not request historical replay.

The first finish starts work immediately. Coalescing applies only when a build or
send is already in progress. Frame/cache storage has no per-finish history.
Client admission and active workers are capped at32; frame JSON at256KiB. Each
client retains at most one pending and one in-flight frame reference. Existing
rank maps still scale with race membership, not with connected-client count.

Request cancellation removes the subscriber and pending frame. Last unsubscribe
cancels its view worker and removes its cache/map entry. The one idle publication
dispatcher can remain until unpublish; it performs no polling. Store close,
publication change and application shutdown cancel the dispatcher and all child
contexts. HTTP shutdown waits only after cancellation; an expired shutdown closes
remaining connections. Signal wake channels are never closed, so a concurrent
late writer cannot panic by sending to a closed channel.

## Client ownership

`createFinishFeed` has one generation which owns a JSON request or EventSource.
Visibility, navigation, filter and Refresh invalidate the prior generation and
close/abort its transport. Stale callbacks cannot mutate the new view. Native
EventSource handles recoverable reconnects; permanent errors reload JSON. A
silent-connection watchdog closes the old source before replacing it.

Heartbeats update only liveness. Snapshot reconciliation preserves stable
participation keys, scroll anchors and unchanged cards. Returning/reconnecting
sets an initial-history baseline so past finishes are not newly highlighted.
The fallback poller remains one request at a time against older servers or
without EventSource; stop cancels its timers and listeners as well.

Profiling and allocation/FD/goroutine instrumentation live exclusively in tests.
The runtime LAN API exposes no profiling or administrative mutation route.
