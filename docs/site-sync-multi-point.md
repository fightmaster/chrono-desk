# Multi-point site sync

## Problem

Chrono Desk is one timing point among several. A remote Stopwatch Checkpoint,
an RFID reader and a manual Chrono Desk finish must contribute to one event as
independent producers. Synchronization must merge their raw observations and
then rebuild the derived results from that union.

The reported loss is reproducible with this sequence:

1. Stopwatch Checkpoint sends a mark through rfid-hub. Its `rfid_logs` row has
   the Stopwatch UUID as `id`, the bib in `number`, an empty `epc`, and the
   checkpoint code in `board`.
2. rfid-sync resolves the participant by number and creates the checkpoint
   `results` row.
3. Chrono Desk pushes its own finish logs to run5.
4. run5's `SyncFinalizeJob` runs a full `RecountRfid`: it deletes every derived
   row with a non-null `rfid_log_id` and replays all raw logs.
5. The PHP `PushResult` replay looks up a participant only by `epc`. A
   Stopwatch mark has no EPC, so replay skips it. The raw log survives, but its
   result does not.

This is an algorithm-parity bug, not a reason to make Chrono Desk the owner of
the whole event. Chrono Desk and rfid-sync already use the required rule:
resolve by a positive `number` first, otherwise fall back to `epc`.

## Regression coverage

`TestRecountMergesStopwatchCheckpointWithChronoDeskFinish` builds one race with
a remote number-only Stopwatch checkpoint and an EPC-based Chrono Desk finish.
It recounts twice and requires both results to survive with their original
checkpoint mapping. Besides documenting the multi-producer contract, it guards
Chrono Desk against drifting from rfid-sync's member-resolution semantics.

The corresponding run5 feature regression belongs in
`tests/Feature/Api/SyncEventApiTest.php`:

1. create a remote checkpoint and a finish checkpoint on different boards;
2. create a member with both bib and EPC;
3. insert a number-only PWA `rfid_log` and its already-derived remote result
   (the state produced by rfid-sync);
4. POST a Chrono Desk payload containing only the finish log;
5. assert that both the remote raw log and its result still exist after the
   synchronous test-queue finalize.

Before the compatibility fix, the run5 test failed at step 5: the `rfid_logs`
row existed, while the remote `results` row was gone.

## Implemented compatibility fix

run5's `App\Services\PushResult` now follows the same participant lookup as
rfid-sync and Chrono Desk:

- if `rfidLog.number > 0`, find the event member by `number`;
- otherwise, find by non-empty `epc`;
- keep event scoping and the existing race filter;
- run the feature regression above together with the existing recount and sync
  tests.

This makes the bulk replay match rfid-sync and Chrono Desk. The existing sync
merge can then remain additive: run5 inserts unknown raw logs without deleting
logs that came from other points, and the full replay derives the union.

## Replay unification: two different meanings

“Use one replay” can mean either semantic parity or literally one runtime
implementation. They have very different risk.

### Semantic parity (implemented)

The existing PHP and Go replay entry points remain, with one versioned
fixture contract authoritative. The same input members, checkpoints and raw
logs must yield the same checkpoint results and member times in run5,
rfid-sync and Chrono Desk.

This is test-only work and cannot break production synchronization. It detects
drift such as the number/EPC mismatch before a deployment. The first fixture is
`internal/service/testdata/parity/multi-point-replay-v1.json`: the number-only
Stopwatch split plus EPC-based Chrono finish covered here. An identical copy is
executed by run5; rfid-sync consumes the same observations to guard its
event-scoped number-first/EPC-fallback lookup.

Compare only canonical derivation at first:

- result natural identity (`rfid_log_id`, member, race, checkpoint, time);
- member start/finish/clean time;
- disabled-log behavior and checkpoint eligibility/order.

Do not initially compare site-specific read models, UI places or manual result
projection; those are finalization concerns and would blur the replay contract.

### One physical implementation (deferred)

The target architecture should remove the second derivation implementation
from the site. run5 should initiate an event replay handled by rfid-sync's
existing replay processor (the same processor that handles live Redis events),
then run only the site-owned finalization steps for manual results and read
models. The current `rfid-sync/cmd/replay` is the natural starting point; it
needs a production-safe, per-event serialized invocation rather than shelling
out from a web request.

Until that orchestration exists, the minimum safe definition of “one replay”
is a shared set of golden fixtures executed by all three implementations. Each
fixture must contain the same raw logs/checkpoints/members and require identical
`results` plus member timing. The Stopwatch-number + Chrono-EPC fixture added
here should be the first cross-repository case.

This physical consolidation is not required for the reported bug and should not
be combined with the compatibility fix. Before attempting it, define ownership
of `results`, member compatibility columns, `member_results`, manual results and
failure recovery. Until then, semantic parity fixtures give most of the safety
without adding a runtime dependency between run5 and rfid-sync.

## Disabled flags: targeted sync schema v2

Sync schema v1 has a second multi-writer risk. With
`overwrite=true`, disabled flags are reconciled as if the Chrono Desk payload
were a complete authoritative event snapshot: every site log absent from the
payload's disabled-id list is re-enabled. A stale desk can therefore undo a
judge action made on a different point or on the site.

Schema v2 implements an explicit delta in the sync contract:
`rfid_log_edits: [{id, disabled_at}]`, built only from Chrono Desk's
`local_changes` journal. run5 updates only those named, event-scoped ids;
absence never means “enable”. `disabled_at: null` is an explicit re-enable.

This change does not alter `rfid_logs`, its id formula, Redis events or the
rfid-hub/rfid-sync insert-only contract. It only changes the Chrono Desk → run5
HTTP payload for judge edits. Raw marks remain additive from every producer.

A safe rollout is server-first and fail closed:

1. run5 advertises authenticated capabilities and accepts schemas v1 and v2;
2. v2 tests prove explicit disable/re-enable, site-only state preservation,
   idempotent retry, overwrite gating and insertion of disabled new logs;
3. Chrono Desk performs a capability preflight and sends v2 only when supported;
4. an old server or a v1-only server causes the desktop to stop before POST;
5. v1 remains only for deployed old clients and can be retired separately.

With v2, `overwrite=true` means “apply the explicit desktop journal”, not
“make the whole event equal to this snapshot”. New raw logs and manual results
remain additive from all timing points. `overwrite=false` still suppresses
destructive field and RFID-log edits.

## Observation ownership journal and push v3: implemented

Chrono Desk now distinguishes local capture from imported storage before the
v3 transport is enabled:

- live TCP and local Feibot CSV acceptance use `InsertOwnedRfidLogs`;
- the new `rfid_logs` row, origin metadata and `observation_outbox` row commit
  in one SQLite transaction;
- a duplicate id already imported from the site is left foreign and does not
  acquire an outbox row;
- snapshot import/pull continues through the non-owning upsert path;
- one installation UUID and monotonic sequence are persisted outside the
  per-event databases, so retries and restarts cannot renumber accepted rows.

The push handler now prepares at most one stable `sent` batch from that outbox,
builds schema v3 without `rfid_logs`, requires RUN5 to advertise v3, and applies
only a complete matching item acknowledgement. A lost response leaves the same
batch `sent`; observations captured meanwhile remain `pending` for the next
batch. `inserted`/`duplicate` become `acked`, while `rejected` remains visible
as a terminal journal state. A v3 edit-only push omits `observation_batch` and
does not fall back to the legacy full snapshot.

Old Chrono Desk clients can still use server v2 during the deployment window.
This client fails closed against a server without v3 so it never regresses to
re-sending downloaded observations.

## Incremental pull v1: manual path implemented

The manual pull first imports the ordinary event export as an idempotent
bootstrap and then drains `/api/sync/events/{event}/changes` from the last
opaque cursor. Each page is validated and applied with its `next_cursor` in one
SQLite transaction. A raw identity conflict rolls back the page; an imported
row never creates `observation_outbox`. Overlap with the snapshot fills missing
origin metadata but preserves Chrono Desk ownership already recorded locally.

After the feed is drained Chrono Desk classifies the complete committed batch and
executes one coalesced projection plan. Exact duplicates and unmatched history do
not trigger a recount. Inserted or state-changed observations with an unambiguous
bib/EPC trigger at most one member replay per participant, regardless of how many
rows arrived; a race/event scope subsumes its narrower actions. Member resolution
is cached per identity while planning, so an 11,000-row upload for one participant
does one lookup and one member replay, not 11,000 SQL lookups or replays.
Plans affecting more than 500 distinct participants, or more than one whole race,
collapse to one event replay; this avoids repeated event scans and SQLite bind-list
limits while keeping the common one/few-participant update cheap.

The plan is bound to SHA-256 evidence for the exact projection configuration and
input snapshot. The configuration hash includes event/race age policy, categories,
race-category links, member `category_id`/gender/date of birth and checkpoint
rules. The input watermark includes raw observations, manual results and the pull
cursor. Evidence is recalculated after acquiring the SQLite write transaction; a
mismatch falls back to one full event replay. Ambiguous bib/EPC lookup also fails
closed instead of allowing SQLite's row order to select a participant.

Every feed transaction that inserts an observation or changes its disabled state
also sets durable `sync_config.projection_pending=1` alongside the new cursor. The
flag is cleared only inside the successful replay/plan transaction. Therefore a
crash after cursor commit but before projection cannot lose work: the next pull
detects the flag and performs one evidence-bound full-event recovery replay. A
crash after replay but before the HTTP response may cause a retry, but not a missing
result. Metadata-only duplicates do not set the flag.

Starting live Feibot ingest starts a five-second background pull loop; stopping
the live session cancels it. The manual button and loop share a per-event mutex,
while the SQLite transaction protects against other local writers. Empty and
duplicate-only polls do not rewrite the protocol.

Chrono Desk stores explicit start provenance beside `members.start_time_ms`:
`unknown` for imported/legacy values, `manual` for judge edits,
`race_default` for fallback to the race start, and `observation` plus
`start_observation_id` for an RFID START. Full and targeted recount clear only
the two derived sources before replay and preserve `manual`/`unknown`; disabling
the observation that supplied a start can therefore remove it without erasing a
judge decision. RUN5/rfid-sync adoption remains a coordinated expand-only
follow-up before this provenance can cross the site sync boundary.

## Replay concurrency: a Laravel lock is not enough

Two run5 finalize jobs can overlap, but a Laravel-only `WithoutOverlapping`
lock would cover neither rfid-sync's live processor nor another non-Laravel
writer. `ShouldBeUnique` is worse: it may discard the second replay after its
raw logs were committed, leaving those logs underived if the first replay had
already loaded its input snapshot.

Any real serialization design must coordinate all derivation writers while
allowing raw ingestion to continue. The desired boundary is per event:

1. acquire a cross-service replay lease/fence;
2. rfid-hub/rfid-sync may continue durably inserting raw logs, but derivation
   for that event is deferred or ordered behind the replay;
3. replay a defined input watermark;
4. publish/finalize the derived state;
5. release the fence and process raw logs that arrived after the watermark.

Possible mechanisms are a database-backed event replay lease understood by
both PHP and Go, or moving bulk replay into the rfid-sync process so its own
per-event scheduler orders live and bulk work. This is a separate architecture
change. No concurrency lock should be added until both sides participate and
crash recovery, lease expiry and queued catch-up have tests.
