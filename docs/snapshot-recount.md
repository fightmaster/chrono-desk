# Shared snapshot recount and independent reads

Task: CHR-DESK-PERF-001
Docs-Impact: CROSS_PROJECT

The current source pins published `timing-core v0.11.0`
(commit `65793ffc88f3466b875293af58c04996d2b55bfc`) and retains its strict
`event-snapshot-fixed-distance-v1` calculator. The initial adapter shipped with
core v0.9.0; the compatibility update is described in
[timing-core 0.11 adoption](timing-core-0.11-adoption.md). The same pure package is
consumed by rfid-sync's numeric-ID wrapper; Desk keeps opaque string IDs. SQL,
transaction lifetime, durable raw storage and publication remain consumer
responsibilities. Both shared modules remain immutable published dependencies,
without a local `replace` directive.

## Calculation and atomicity

`LoadFixedDistanceSnapshot` loads races, members with start provenance,
checkpoints and manual finishes in four queries. An ordered observation cursor
retains disabled facts as inputs without accumulating all raw rows in memory.
The shared engine computes checkpoint selection and timing. Materialization
writes batches of 128 derived passes/member timings, below the historical SQLite
999-parameter limit. It never modifies raw observations, manual results, disabled
flags, registration or producer journals. Existing explicit derived replacement
is retained; it is not deletion of raw/judge facts.

The entire operation remains in the existing SQLite writer transaction. Failed
inserts/updates roll back the complete replacement; readers see the preceding
committed projection until commit. A race-scoped replacement preserves all other
races and their reserved observations. Unsupported formats/statuses/manual
checkpoint profiles use the established Go replay; real cursor/materialization
errors propagate and must never silently switch calculator. Single-member
planned replays retain the existing narrow Go processor.

This first adapter removes per-observation SQL from full/race recounts. It does
not yet move calculation outside the writer transaction, implement staged
publication or promise zero ingest wait. Those changes require revision checks
covering every writer; a process mutex alone is insufficient.

## App-owned ingestion and UI-owned reads

The event catalog owns one writer and a separate pool of four query-only reader
connections, with pragmas on every connection. UI use cases pin one coherent
read-only transaction and release it before JSON/network output. Nested reads
inside a command retain that command's transaction and read-own-writes. Closing
the catalog closes both pools. A waiting read is cancellable; slow clients do not
hold a read lease while consuming HTTP responses.

Feibot/Edge listeners and camera pollers are app-owned background services. They
start with their own background context and continue after Live unmounts. Only
explicit Stop or app shutdown stops ingestion. The UI cancels only display GETs;
it never stops ingestion as a side effect of navigation.

Camera polling transfers finish metadata and frame URLs. Image bytes are fetched
on thumbnail/view/export demand and cached locally. Returning to Live requests
40 recent chip reads and up to 60 photos/merged finishes. Manual judge rows remain
uncapped in the current feed; photo merge/status still scan all local metadata.
These are explicit follow-up optimization points, not bounded-work guarantees.

Live keeps one active refresh plus at most one pending refresh per feed/photo
group. Status and feed load concurrently; photos run independently. All photo
requests settle before the next group starts, including when one fails early.
Destroy cancels in-flight display work and drops the pending refresh. An old GET
status cannot overwrite the result of a newer Start/Stop command.

## When recount runs

A new reader observation is persisted and processed once for its participant;
a duplicate does not replay the event. Photos and protocol/tab GETs do not trigger
recount. Explicit recount, derivation-affecting UI edits and full site re-import
can replay the whole event. Several single-participant UI edits still request a
whole-event recount; narrower audited commands are a remaining improvement.

While admission is running, site change polling runs every five seconds. It
builds member/race/event replay plans only when needed. Recovery, ambiguous
identity or changed exact/revision evidence can escalate to full event replay.
An empty feed is not a full recount every five seconds, although exact evidence
hashing itself still scans inputs and uses the writer handle.

## Evidence and remaining acceptance

See [the measured audit](reports/chrono-desk-performance-2026-10-19.md) and its
aggregate JSON. Native owner-Mac parity compares complete protocols/places with
all personal data retained on that Mac. Isolated Mac HTTP replay demonstrates
writer/read independence. Tests hold an uncommitted replacement to prove every
critical view reads a complete prior state, verify query-only/cancellable reader
connections and inject materialization failures to prove rollback. Background
transport tests persist 100 TCP reads and 120 camera metadata records without any
Live display requests, then check limited display responses and zero image fetches.

The candidate is not installed. Mac WebView/physical reader freshness, repeated
full-load measurements, crash/restart acceptance, shorter revision-checked
publication, no-op hash reduction and photo merge caching remain in the central
task. Go1.24.13 remains required for the owner's macOS11; its existing standard
library vulnerability debt is tracked by CHR-SEC-002, not resolved by this change.
