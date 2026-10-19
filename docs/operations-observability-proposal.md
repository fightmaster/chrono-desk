# Competition operation: focused observability proposal

Tasks: CHR-DESK-PERF-001, CHR-DESK-UI-001, CHR-OBS-001, CHR-ADJ-001, CHR-OPS-002
Docs-Impact: LOCAL

This is a design proposal, not a claim of implementation or field acceptance.
It prioritizes explaining and recovering timing failures over further shaving
milliseconds from the now-bounded full recount. The central TODO owns scope and
status; the local document describes UX, evidence and proposed resource budgets.

## First release scope

1. **Who is still expected.** Per-distance on-course roster and explicit finished,
   on-course, DNS, DNF and DSQ tallies (CHR-DESK-UI-001). Exclude reserves and future
   starts; label unknown/incomplete timing separately. A mass-start expectation
   does not prove a physical start, and silence never automatically becomes DNS.
   Preserve official ranking/CSV and card navigation. Derive one coherent read
   response in bulk, no per-participant SQL or writes.
2. **End-to-end operating status.** Reuse the existing heartbeat and `/live/status` rather than introducing a
   second status endpoint or duplicate polling. Inspect existing payload fields
   before adding anything. A compact view of listener state,
   reader heartbeat age, last arrival/commit age, inserts/duplicates/errors,
   local outbound pending count/oldest age, site acknowledgement and publication
   freshness where actually supported. Existing counters/outboxes remain the
   source. “Listener started”, “reader alive”, “stored locally”, “accepted by
   site” and “published” must be distinct; unavailable evidence is unknown,
   never green. Silent periods without expected tags are normal; warn about lost
   heartbeat/connection or growing backlog rather than time since last runner.
3. **Bounded incident timeline and export.** Record connect/disconnect/restart,
   errors, reader/session identity, version/config changes, recount reason/scope/
   duration and periodic aggregates. Use a separate rotating file/ring, not the
   event SQLite writer. No names/EPC/payload/token/camera images. A full queue may
   coalesce routine diagnostics; timing records must never be dropped to protect
   logs. Export an allowlisted bundle explicitly on request. Capture timestamp
   and host receive/commit timestamps are different clocks; do not call their
   subtraction network latency.

Proposed initial budgets, **not measured guarantees**: operation counters and
latency buckets under1% added CPU in the representative ingest workload;
telemetry buffers/caches at most5MiB; no extra per-reading diagnostic SQL; existing
heartbeat/status refresh only, no additional status endpoint or duplicate polling
from components; periodic diagnostic summaries every5s plus state transitions, bounded
10–20MiB on-disk retention. Keep receive/commit timing locally; do not log every
observation. Compare disabled/enabled runs and p95/p99 before accepting these
numbers; retain a bounded failure signal even if diagnostic writing fails.

## Follow after basic operation is visible

- **Pre-start report.** Explicit command checks duplicate chip assignments,
  missing/incompatible checkpoint bindings, race start/timezone/event identity,
  listener availability, free disk and sync target. Reuse bulk registration and
  config reads. Query devices only when supported, with bounded parallelism and
  timeouts; an unreachable device is unknown. No new work per reading and no
  automatic admission stop/time rewrite. Children without chips/manual distances
  are valid configurations, not universal failures. Duplicate chip flags require
  human review of event/race/session scope, never automatic deletion.
- **Why this read was not counted.** The card distinguishes disabled-by-judge,
  unknown/ambiguous participant, wrong checkpoint source, before-open, repeated
  checkpoint and after-final-result cases. Show the relevant rule/value and next
  useful action. Compute a bounded trace lazily on the requested history, not
  by replaying the event for every poll. Distinguish current-rule explanation
  from a recorded original decision; do not invent historical causality when
  the old configuration is unavailable. Aggregate reason counters are cheap;
  a durable explanation row per raw read is excluded from first release.
- **Backup and tested restart/recovery.** Existing manual backup remains. Use a
  coherent SQLite backup API including WAL state; never copy the main file alone
  during active writes. Add pre-start/end/config-change snapshots, then changed-
  data periodic copies only after measuring IO. Start with a small retained set
  (e.g. last3 plus pre-start/end), not an unbounded five-minute archive. Restore
  into a separate local directory and verify counts/protocols/ownership; never
  overwrite the working event as a test. Audit the native raw/outbox → initial
  projection crash gap and retransmit semantics before claiming full recovery.
  The Edge path already joins acceptance and first projection in one transaction.

Budgets for these actions are command-triggered or background-bounded, not part
of the reader hot path. Backup peak disk/IO must be measured on the owner Mac;
28MiB copies every5min would write about336MiB/hour before compression and are
not a free operation. An independently recoverable copy/device is a separate
operational decision; this proposal authorizes no off-device participant transfer.

## Acceptance that buys practical value

Run reader restart, lost camera, site unavailable, slow/failed disk, duplicate
storm and app kill/restart scenarios on copies. Ingest must persist with Live
closed; a return displays a bounded latest window. Status must identify which
stage stalled without exposing participant payloads. Diagnostic IO/queue pressure
must not stall admission. Compare protocol/places/protected facts with telemetry
on/off; measure receive-to-commit, critical GET, display update, CPU/RSS and disk
rate. No claimed end-to-end latency without an actual receive timestamp and UI
trace. Postpone animations, another database and pervasive tracing unless these
measurements show a need.
