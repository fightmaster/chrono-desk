# Architecture

## Goal

A multiplatform (macOS primary, Windows, Linux) desktop app that replaces the run5 server
in offline zones at competition sites: view results, top-3, print protocols, make
temporary registration fixes — without internet access.

## Non-goals (v1) — and what shipped since

The site stays the source of truth throughout; these were the deliberate v1 cuts, with
their current status noted.

- Bidirectional sync / conflict resolution. *(v1 deferred.)* **Shipped in v0.3** as opt-in
  push-back of local edits, logs and manual finishes to run5 (token-auth, idempotent,
  overwrite-gated where destructive). Push schema v2 sends RFID disable/re-enable actions
  from the `local_changes` journal explicitly; absence from a desktop snapshot never changes
  a site log. Chrono Desk checks the authenticated run5 capabilities endpoint and fails closed
  before sending if v2 is unavailable. **Current v3 push** now sends raw observations only
  from the durable ownership outbox, requires advertised v3 support and persists item-level
  acknowledgements; it fails closed instead of falling back to a full-log v2 snapshot. The
  site remains source of truth; conflict policy for
  fields explicitly edited offline is **local edits win** — a re-import replays the journal on
  top (`ReapplyLocalEdits`).
- Ranking parity for every run5 race format at once. v1 shipped FixedDistance plus both
  category-ranking strategies; **TimeLimited shipped** as well. Run5Stopwatch is specced
  (`docs/ranking.md`) but not yet implemented — `BuildProtocol` fails closed on it.
- Replacing rfid-hub on site. Still a non-goal. But the desktop is no longer flash-CSV
  only: **live TCP ingest shipped in v0.2**, built on the shared `rfid-core` module.

## Layers

```
frontend/ (Svelte + Vite)  — UI, talks HTTP to localhost
   │
main.go / app.go           — composition root
   │
internal/transport/httpapi   — REST API (embedded localhost; control surface for the UI)
internal/transport/publicweb — separate read-only LAN results board (off by default)
   │
internal/service           — import/list use cases, recount orchestration, ranking, excel export
   │
internal/processor         — SQLite adapter around shared timing-core
   │
internal/domain            — Event, Race, Category, Checkpoint, Member, RfidLog, Result
   │
internal/infrastructure/sqlite — event catalog, repositories, schema, migrations (modernc.org/sqlite)
```

Wails v2 hosts the frontend and the Go process in one binary (pattern proven in
RaceTorchApp: `app.go` exposes only `APIBaseURL()`; everything else goes over HTTP).

## Key decisions

1. **One event = one SQLite file** (e.g. `marathon-2026.chrono`). Portable by flash
   drive between laptops, backup = file copy, corruption is isolated per event.
   The file-system concerns and open-store cache now live in `sqlite.EventCatalog`;
   application-level import/list logic stays in `service.EventService`.
2. **Reuse one versioned timing-core, not a copied processor.** The immutable
   `timing-core v0.8.0` release owns `legacy-once-v1` checkpoint eligibility,
   `member-time-v2-start-provenance`, `result-outcome-v2`, deterministic
   `ranking-v3-canonical-decimal-id`, age calculation and the
   ordered `impact-v3-preview` projection plan. Chrono Desk keeps the
   SQLite repository/transaction adapter; rfid-sync keeps its MySQL adapter.
   run5's `PushResult`/`RecountRfid` (PHP) remains the migration reference until
   production parity and retirement. Ranking input conversion remains local,
   while ordering and place assignment use the shared core; full policy and
   source paths are in `docs/ranking.md`.
3. **HTTP API instead of Wails bindings.** Gives LAN access for judges' tablets later,
   headless mode for free, and keeps the frontend decoupled.
   The transport is wired in `app.go`: `httpapi.New` receives ready `LiveManager`,
   `PhotoManager`, `PhotoCache`, event service and public broadcast server instead of
   constructing runtime dependencies internally.
   - The localhost control API requires a random per-process bearer token. The Go host
     passes it to the embedded frontend through Wails bootstrap bindings; Svelte sends
     it in `Authorization` on every control request. The token lives only in memory,
     changes on every launch and is never included in a QR code.
   - **LAN results broadcast is a *separate* read-only server**, not the control API
     opened up (`internal/transport/publicweb`, default `:8090`, `CHRONO_PUBLIC_PORT`).
     The control API has many mutating endpoints (recount, edits, manual finishes,
     deletes, sync **with the run5 token**) that must never reach a public venue Wi-Fi, so
     the broadcast exposes only GET routes over a **PII-trimmed** projection — no date of
     birth, no internal ids — built by reusing `service.BuildProtocol`. It is off by
     default and binds its `0.0.0.0` port only while the operator has the broadcast
     switched on (per event), closing it again on stop. The settings screen shows the
     LAN address and a QR code (PNG via the pure-Go `skip2/go-qrcode`).
     The broadcast deliberately remains tokenless: its HTML and read-only API share
     one origin, and possession of the LAN QR link is the intended access mechanism.
4. **Idempotency contract.** `rfid_logs.id = md5(board + epc + timeMillis + ant)`
   (string concatenation, no separators; board like `"Feibot:U659"`, EPC uppercased and
   trimmed, time as unix-millis decimal string, ant as decimal string). For number-based
   devices: `md5(board + number + timeMillis)`. Source of truth:
   `rfid-hub/internal/tcp/adapters.go` (`parseRFID`); run5 `app/Models/RfidLog.php` must
   match byte-for-byte. Logs collected by chrono-desk can therefore be uploaded to the
   site (and vice versa) without duplicates.
   **Caveat (discovered 2026-06-06):** historical site data contains legacy ids that
   pre-date this formula (~57% of event 621632's logs), so the id PK alone cannot dedup
   a flash drive against an event export — the Feibot CSV importer additionally dedups
   by content key `epc|time_ms|ant` per board, exactly like run5's `loadExistingKeys`.
5. **Times are unix milliseconds everywhere internally.** Feibot flash-drive CSV carries
   local wall-clock time without zone — the import dialog requires an explicit event
   timezone (defaulted from the event export).
6. **Disabled logs are part of the contract.** run5 ADR-0007 soft-disables false reads
   (`disabled_at`). Exports include disabled logs; recount skips them.
7. **Local mutations and their journal entries are atomic.** Application use cases run
   through `sqlite.Store.WithinTx`; the entity update, derived companion updates and all
   `local_changes` rows commit together or roll back together. This covers edits (including
   race-start shifts), member/checkpoint creation, checkpoint deletion, category links and
   manual finishes. Persistence helpers join an existing transaction instead of opening a
   second SQLite connection. Failure tests force journal inserts to fail and verify that no
   partial live data remains. Recount likewise keeps its wipe, RFID replay and manual-result
   reapplication in one transaction, so a failed replay preserves the previous protocol.
   Live checkpoint progression also reads duplicate/disabled state, resolves the
   member, reads previous passes, selects the next checkpoint and writes the
   result inside one SQLite transaction. The single-connection event store
   serializes competing local transitions; a concurrent mixed number/EPC test
   proves one logical `once` checkpoint is not accepted twice. Member lookup reads
   at most two matches and fails closed when a bib or EPC is ambiguous; choosing an
   arbitrary `LIMIT 1` row is forbidden because it could project a pass into the
   wrong participant or race.
   Incremental sync also commits a durable `projection_pending` bit with every
   projection-relevant cursor advance and clears it only in the successful
   projection transaction. Recovery after a process crash therefore escalates to
   one full event replay instead of silently skipping already-cursored input.
   Planning and execution read exact SHA-256 evidence together with
   transaction-coupled `projection-revision-v1` counters. Exact hashes remain
   authoritative; if only one evidence form changes, Chrono Desk logs the parity
   mismatch and fails closed to a full event replay. Field acceptance is keyed
   by event, revision contract, deliberate acceptance window and application
   build, so a new algorithm or release cannot inherit older samples. Committed
   checks remain in the projection transaction; a failed transaction records a
   separate durable attempt after rollback. Both current and historical windows
   are exposed by the authenticated sync-config endpoint.
   The same endpoint reports main database, WAL, SHM and total byte sizes by
   filesystem metadata only. It neither checkpoints SQLite nor exposes local
   paths, allowing non-perturbing field growth measurements from the UI.
8. **Application ports are declared next to their consumers, not in SQLite.**
   `service.BuildProtocol` depends on a small `ProtocolStore`; recount, local edits and
   event import use similarly narrow consumer-side ports with thin SQLite adapters inside
   `internal/service`. This keeps SQL details and transaction wiring in infrastructure
   while the use cases depend only on the queries and commands they actually need.
9. **Locally accepted observations have a durable ownership journal.** Live TCP
   reads and Feibot CSV rows are inserted together with an `observation_outbox`
   row in one SQLite transaction. Site snapshot/pull imports use the foreign
   insert path and never acquire desktop ownership, including when the same
   deterministic RFID id is later seen locally. One installation identity and
   monotonic sequence are persisted in `.observation-origin.sqlite` alongside the
   event databases; sequence gaps after a rolled-back event transaction are
   valid, reuse after restart is not. Sync v3 assigns pending rows to a stable
   batch before HTTP, retries the same `sent` batch after a lost response and
   moves rows to `acked`/`rejected` only after a complete matching server
   acknowledgement. Imported rows are therefore never uploaded by the v3
   client. A server without v3 capability fails closed; deployed v1/v2 clients
   remain server-compatible during rollout.

## Unpublished edge receiver (CHR-SIDE-002)

An opt-in second live profile reuses `rfid-core/edge` and its shared TCP listener.
It validates explicit event/board/source-session provisioning. New local edge
raw input, its full source envelope in `edge_observation_outbox` and initial
shared-engine projection commit atomically before ACK. Existing rows keep their
judge flags and origin; imports never acquire relay ownership. This separate
journal must not enter the existing Desk-owned v3 batch. `EdgeRelayManager`
forwards its immutable packets through the shared `tcp.LineClient` to an explicit
Hub edge input. Saved per-event settings resume independently of input/site pull;
audited revisions and per-row leases fence stale configuration and ACKs. Missing
ACKs remain pending with persisted bounded retry. No new database, producer
identity, account system or Internet device-command path is introduced. The relay
is locally implemented, not yet cross-process/field accepted with central storage.
Native Feibot input remains independent and unchanged by default. See the
[receiver guide](edge-receiver.md) for limits and compatibility boundaries.

Export v3 and change-feed v1 now retain origin and optional edge metadata in
nullable raw columns. A small domain mapping validates stored edge facts through
the pure shared-core contract; it does not reconstruct outbound source payloads.
Snapshot and feed share one transactional import rule, preserving first-writer
ownership, historical physical aliases and explicit site disable state. Invalid
or partial envelopes abort before cursor acceptance. Imported observations never
enter either outgoing journal. The five edge-only metadata fields are not
projection inputs, so the existing evidence/revision contract is unchanged.

## Data flow (v1)

```
run5 site ──(event export JSON)──▶ chrono-desk import ──▶ event .db file
Feibot reader ──(USB flash CSV)──▶ CSV import (dedup by id) ──▶ rfid_logs
                       recount: wipe derived results → replay rfid_logs
                       sorted by time through the processor → results,
                       member start/finish/clean times → ranking → UI / Excel
```

v0.2 (shipped): Feibot ──TCP──▶ chrono-desk listener (`rfid-core` adapters with a SQLite
`Publisher`) ──▶ rfid_logs → in-process recount → live standings; judges enter manual
finishes and tune checkpoints on the Live screen.

v0.3 (shipped): chrono-desk ──(sync push/pull, X-SYNC-TOKEN)──▶ run5 — local edits, new
members, logs and manual finishes sync back; run5 applies them (overwrite-gated where
destructive). Current push schema v3 uses a capabilities preflight, sends only desktop-owned
raw observation batches plus explicit edits and persists item acknowledgements. Manual pull
uses the full import as an idempotent bootstrap, then drains RUN5 change-feed v1. Every page
and opaque cursor commit in one SQLite transaction; imported observations bypass the ownership
outbox. A live session polls the feed every five seconds. Manual and background pulls share
one per-event mutex, so cursor application and compatibility recount never overlap. Site stays
the source of truth.

Projection evidence currently remains an exact SHA-256 snapshot at planning
and execution. `projection-revision-v1` adds transaction-coupled SQLite shadow
counters through versioned triggers over every evidence table. They are not an
authority switch: exact hashes stay fail-closed while dual-read parity and
legacy-database migration coverage are collected.

Parity telemetry is operational evidence, not projection state. Any committed
mismatch or replay failure marks its acceptance window as blocking an authority
switch. Exact SHA scans cannot be removed merely because a later build has clean
samples.

LAN broadcast (shipped): spectators' phones ──HTTP──▶ chrono-desk `publicweb` (read-only,
PII-trimmed) — distance dropdown + Призёры/Протокол tabs (absolute & age-group podiums,
searchable protocol), auto-refreshing; the Призёры tab copies a Telegram-ready Markdown
winners list for SMM. Operator toggles it per event from the settings screen; the port is
open only while on.

Packet issuance (CHR-SW-009/CHR-SW-010 local candidate, not released): the independent
`internal/packetissuance` domain validates the shared ordinary operation-v1 envelope and
canonical hash without HTTP or SQLite dependencies. The event database stores a
lossless registration projection, immutable operation outcomes and an ordered
issuance feed in tables separate from both legacy `local_changes` and timing
outboxes. Projection + operation + feed commit atomically; a retry after a lost
response returns the existing outcome. The canonical chrono-docs operation
fixture is pinned byte-for-byte in its package testdata. The site relay client is
now a local candidate: it converts the already configured event sync token into a
narrow 30-day grant, stores its pre-generated credential in an installation-private
database outside `.chrono`, installs the strict bootstrap and sends the durable
operation outbox with the ordinary site-sync action. Matching terminal receipts
are committed atomically; lost responses are retried with the same operation and
credential. The incoming site feed has its own per-event cursor and immutable
action journal: a page, compatible three-way projection changes and its cursor
commit atomically, while incompatible local branches remain unchanged and become
review records. Timing evidence is never accepted from the feed and registration
deletion is a projection tombstone, not destructive timing-history deletion. A
normal site pull and the existing live-session background pull also advance this
feed; a manual site push performs the same serialized pull after delivering the
local operation outbox. Network bootstrap-v2 stores the roster and the receiver's
committed feed cursor from one SQLite transaction, so a newly connected tablet
does not replay history already present in its snapshot.

The trusted incoming feed additionally accepts the frozen schema-v2
`resolve_conflict` operation; tablet uploads and the Desk outbound journal remain
schema v1 only. Desk verifies that the referenced conflict occurrence is already
present, applies the complete convergence transition with the existing three-way
merge, stores a unique immutable resolution-to-input relation, and closes that
input without rewriting it. A conflicting third local branch remains a review.
Received site operations are marked acknowledged and never enter the site
outbox. Site-only actions, including resolutions and ordinary server changes,
are copied into the Desk-local ordered feed with their original action identity
and a new local sequence, so LAN-only tablets can converge through Desk. A site
echo of an operation already published locally is retained as separate site
occurrence evidence and is not duplicated in the LAN feed. Projection, both
occurrence journals, resolution relation, LAN relay action and site cursor are
one SQLite transaction.

The current candidate also keeps the last authenticated site registration
projection separately from the locally merged projection. Only an exact,
bounded JSON `409 cursor_ahead` or `409 cursor_expired` response can request a
fresh authenticated bootstrap. Rebase is a three-way merge from that separate
site value: pending local operations and causal heads remain unchanged,
same-field conflicts stay visible for review, and absence from a snapshot never
deletes a registration. The new baseline, cursor, merged projection and private
rebase evidence commit atomically; old databases with prior issuance history
and no provable site baseline fail closed instead of inferring one.

Desk's tablet feed has a monotonic `first_available_sequence`. Explicit local
retention keeps the newest 10,000 live actions and moves at most 100 contiguous
older actions per transaction into a compressed private archive. It preserves
action identity, order, outcomes and causal dependency admission. Archive
insertion, live deletion and floor advancement are atomic; a client below the
floor receives exact `409 cursor_expired` and performs the same pending-safe
bootstrap rebase. Execution is unavailable while the LAN listener or an
unrevoked tablet grant/invitation is active. There is no automatic pruning.

Ordinary Desk member edits now join that same active-scope boundary. A relevant
profile/status/bib/EPC/race edit updates `members`, the legacy `local_changes`
audit, the lossless packet projection and one `chrono_desk.local_edit`
`server_change` in a single SQLite transaction; a feed failure rolls everything
back. Timing-only fields do not create packet traffic. A local walk-in cannot be
created after a site-backed issuance scope is installed because the legacy sync
cannot safely replace its `local-*` identity with the server registration ID;
the operator uses an existing reserve slot instead. Before scope installation,
the existing walk-in workflow remains unchanged. A subsequent legacy event
export may refresh timing/configuration, but registration fields advance only
through the packet feed and the old local-wins replay is disabled for that active
scope. This prevents an export lacking issued/reserve/causal-head data from
silently replacing or resurrecting issuance state.

The dedicated `internal/transport/packetlan` candidate exposes only packet
issuance over TLS on `chrono-desk.local` (default port8443). It never mounts
localhost controls or tokenless public results. One installation-owned CA is
created atomically outside portable event files; owner-only Unix modes or
Windows ACLs protect both private keys. The operator enables exactly one event.
That enabled choice survives a Desk application restart, while stop closes HTTPS
and mDNS before persisting disabled state. Each tablet has an event-scoped,
hash-only, revocable10-minute invitation/7-day credential. Claim throttling,
bounded headers/bodies/feed pages, exact CORS/preflight and real local TLS tests
are part of the adapter. Physical Android/iPhone CA installation and mDNS
resolution remain release gates; the localhost bearer is never sent to a tablet.

## Checkpoint semantics (inherited from run5 / rfid-sync)

- Types: 1=START, 2=CHECKPOINT, 3=FINISH. Checkpoints belong to a race, are bound to a
  physical reader via `board`, ordered by `sort`.
- A log produces at most one result: the first eligible checkpoint in sort order that is
  not yet passed, past its activation time (`since` or member start + `since_offset_seconds`),
  past the sleep window after the previous result, and with `sort` greater than the last
  passed checkpoint.
- START sets `member.start_time` with `observation` provenance and its raw log
  id; FINISH sets `member.finish_time` and computes `clean_time`
  (`HH:MM:SS.mmm`). Race fallback and judge starts use distinct provenance so
  recount clears only derived starts.

## Build & platforms

- **Development machine: Ubuntu.** Wails on Linux needs `libwebkit2gtk-4.1-dev`; build
  and dev runs require the `-tags webkit2_41` flag (Ubuntu 24.04+ ships webkit2gtk 4.1,
  not 4.0).
- **Competition machine: MacBook Pro Retina mid-2014, Intel i5, macOS 11.7 Big Sur**
  (the model's last official macOS). Wails v2 needs macOS 10.13+ — fine. Build target:
  `darwin/amd64`.
- **The embedded frontend targets Safari/WebKit 14 explicitly.** Big Sur is the
  compatibility floor, so Vite's moving default browser baseline is not used.
  Svelte 5 applications must start through `mount`; the quality gate opens the
  built bundle in headless Chromium and requires the rendered application shell
  with no uncaught exception. This catches bootstrap failures that compilation
  alone cannot detect. Static fallback content remains visible if JavaScript
  cannot mount, instead of leaving an unexplained empty dark window.
- **Go toolchain is pinned to 1.24**: Go 1.25+ binaries require macOS 12 Monterey and
  will not run on the competition MacBook (go.dev/doc/go1.25). The pin lives in the
  Makefile (`GOTOOLCHAIN=go1.24.13`) because the go.mod `toolchain` directive cannot
  downgrade below a newer locally-installed Go. Build through `make`. Do not bump until
  the competition Mac is upgraded (e.g. OpenCore Legacy Patcher → Monterey+) or replaced.
- **Accepted consequence of the pin**: go1.24.13 is the final 1.24 patch (out of the
  security window), so `govulncheck` permanently reports stdlib vulnerabilities fixed
  only in 1.25+. Accepted for an offline desktop app whose HTTP API binds to
  localhost/LAN; do NOT "fix" by bumping Go. Re-evaluate when the Mac constraint goes.
- The 2026-08-21 audit still reports `x/net` findings whose patched releases
  require Go 1.25. They remain explicit in `make audit`; they are not represented
  as a green security gate. The former Excelize dependency was removed by
  replacing protocol XLSX generation with standard-library CSV generation. The
  control API remains localhost/LAN-only. Upgrading the competition Mac and Go
  is still required to close the remaining toolchain and network findings.
- The authenticated site/packet client rejects redirects and explicitly disables
  HTTP/2, so event and relay credentials stay on the configured endpoint and the
  Go 1.24 compatibility build does not enter its known HTTP/2 client paths. All
  embedded servers accept HTTP/1 only, and the opt-in packet-issuance TLS listener
  advertises only `http/1.1`; its existing connection, header, body and deadline
  bounds remain mandatory. These controls
  narrow the legacy-toolchain exposure but do not turn `govulncheck` green or
  replace the platform decision above.
- Dependencies are capped by the pin too: `modernc.org/sqlite` is held at v1.44.0
  (v1.45+ requires Go 1.25).
- **macOS builds cannot be cross-compiled from Linux** (CGO + Apple frameworks). Two
  paths: GitHub Actions macOS runners (free tier is enough for release builds) or
  building on the MacBook itself. Windows builds: `windows-latest` runner or
  cross-compile from a host with the toolchain.
- Resilience fallback: because the UI talks to an embedded HTTP API, the core can run
  headless with the UI in a regular browser if the webview misbehaves on old macOS.
- **Private module provenance.** Release workflows fetch immutable
  `timing-core v0.8.0` and `rfid-core v0.4.0` tags with one read-only
  `TIMING_MODULES_READ_TOKEN` GitHub Actions secret. It must be a GitLab
  Personal Access Token with access to both private projects: legacy
  `read_repository`, or fine-grained `Code: Download`. Fine-grained
  `Code: Read` does not authorize Git-over-HTTPS. A project Deploy Token cannot
  span the independent repositories. The edge branch currently pins the
  unpublished v0.4.0 source candidate; its exact tag must be published before
  remote release jobs. Local file-proxy verification is not tag publication.
  A missing/invalid secret fails in a dedicated preflight, and no local
  `replace` is permitted for either release dependency.
- **Release identity.** `VERSION`, full Git revision and the source commit
  timestamp are linker-stamped into `/api/version`; diagnostics also expose the
  timing-core, rfid-core and event-export/sync/change-feed/reader-transport
  contract versions. A `v*` tag must equal `VERSION`. The workflow signs and
  strictly verifies the macOS app,
  records whether the signature is ad-hoc or Developer ID, packages source-
  addressed macOS/Windows filenames and publishes a verified `SHA256SUMS`.
  Developer ID secrets are optional for direct competition use; notarization
  and public trusted distribution remain a separate external credential step.
  For CHR-REL-002, v0.4.3 also fixes native package identity: keep
  `wails.json` `info.productVersion` equal to `VERSION` when releasing.
  A Go regression test enforces this relationship; native CI checks both macOS
  bundle version keys and both Windows executable version fields before
  packaging. Without the explicit Wails setting, packages incorrectly retain
  the framework default `1.0.0` even when `/api/version` is correct.

## Testing strategy

- Port rfid-sync's table-driven processor tests alongside the engine.
- Execute the versioned multi-producer fixture in
  `internal/service/testdata/parity/multi-point-replay-v1.json` here and in run5; rfid-sync
  consumes the same observations to guard number-first/EPC-fallback member resolution.
- **Golden tests against real events**: export a finished event from run5 (rfid_logs +
  reference results/member times) into `testdata/`; the Go recount must reproduce the
  reference exactly. This is the guard against PHP/Go divergence — protocols computed
  offline must match the site.
- Importers (event JSON, Feibot CSV) get fixture-based tests including timezone and
  dedup cases.
