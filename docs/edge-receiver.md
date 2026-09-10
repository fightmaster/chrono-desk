# Owned edge observation receiver

Task: CHR-SIDE-002  
Docs-Impact: CROSS_PROJECT  
Status: local unpublished implementation; receiver acceptance is not end-to-end release acceptance

## Scope

This opt-in LAN input accepts the owned `edge-observation-v1` format from our
sidecar, independent of the reader manufacturer. It reuses `rfid-core/edge`
validation/identity/ACKs and `tcp.EdgeAdapter`; Desk has no second edge parser.
The canonical contract is `chrono-docs/contracts/edge-observation-v1.md`.
Heartbeat and Internet device administration are outside this work.

The existing native Feibot listener stays on default port **5084** with its
existing protocol and limits. The new independent listener defaults to **5085**;
neither starts merely because an event is opened. Both can run simultaneously.
Edge limits are 16 connections, 10,240 bytes per message, 30-second read and
5-second write timeouts, a 256-item bounded queue and one publisher worker.
These are initial limits, not a measured sustained throughput guarantee.

The RFID input is for a trusted venue LAN, not an authenticated public service.
Do not expose it to the Internet. Device/event/session provisioning prevents
accidental routing mistakes; it is not cryptographic device authentication.
Configuration stays on the existing localhost control API with its random
per-process bearer token. No new account, role or SSO system is introduced.

## Operator workflow

1. Import the intended RUN5/Chrono event. Its ID must be a canonical positive
   decimal ID; the selected board must exist in an event checkpoint.
2. Open **LIVE → Sidecar / plate — собственный протокол edge v1**.
3. Add the exact board and source session from the sidecar, then save. At most
   64 boards can be provisioned, one accepted session per board. Unsaved binding
   edits disable Start. A session is not silently inferred from the open event.
4. Set an unused TCP port, configure that Desk address in the sidecar's edge
   destination, and start the edge input. Confirm incoming/inserted counters and
   the actual local result; an edge ACK is not a central RUN5 result receipt.
5. Stop edge input before changing a binding. Stop closes active connections and
   joins their handlers/publisher before returning. It does not stop the native
   Feibot input. **Остановить все входы** stops both profiles for this event.

Event/session mismatch, a removed checkpoint or conflicting immutable content
receives no successful ACK. The source retains its pending delivery. To receive
an older session, deliberately stop and provision that session for its original
event; do not rebind old observations to a new event. Clearing bindings requires
an explicit empty list, not an omitted field.

## Persistence and acknowledgement boundary

The per-event SQLite database gains two additive tables:

- `edge_bindings`: explicit locally audited board/session provisioning;
- `edge_observation_outbox`: the original normalized source envelope plus local
  relay sequence/state, created only for a newly accepted local edge observation.

The raw row, full edge envelope and initial shared-engine projection commit in
one transaction before the shared core can send its scoped ACK. A raw, journal
or projection failure rolls everything back and withholds ACK. Lost ACK/retry
does not insert again, reproject, reset a judge flag or overwrite provenance.
Historical IDs are preserved through bounded physical-content alias lookup;
ambiguous or conflicting content is rejected.

The source's origin/sequence are retained, not replaced with Desk's installation
identity. The full envelope also retains event/session, identity profile and
clock evidence/quality. Native locally created rows continue to use the existing
`observation_outbox` and v3 synchronization. Site imports and an already-known
row never acquire a new edge relay entry.

The raw table also keeps five nullable edge metadata columns. Event export
imports and change-feed pulls preserve them alongside origin v1. Any edge
marker, including explicit null/zero, is validated through shared core; invalid
input cannot be treated as legacy or advance the feed cursor. Both paths use
one transactional import helper, validate immutable conflicts and preserve the
local first writer on physically equivalent native/edge delivery. Only a
compatible missing envelope is enriched; existing facts/IDs/clock decisions are
not rewritten to fit another source. One historical physical alias is resolved,
while multiple candidates reject the whole page. Site disable state is applied
to that existing row. Neither import path creates a relay/native outbox entry.

Clock/session metadata is not an input to timing calculations. Adding only this
metadata leaves both existing exact projection evidence and revision counters
unchanged; event/observation time and judge-state changes retain their current
fences. Readback through recount queries preserves metadata without making it
part of the checkpoint-selection algorithm.

**Relay to the site is not yet connected in this implementation checkpoint.**
The edge queue remains pending and the UI says so. It must not be smuggled into
the current Desk-owned v3 batch, which cannot preserve this source envelope.
Downstream relay/capability support, Hub and central metadata/binding preservation
remain part of the central CHR-SIDE-002 task; they are not deferred to Internet
device management. Do not deploy this receiver slice as a completed new release.

## Local control API

All paths below are under `/api/events/{id}/live/edge` and require the existing
localhost bearer token.

| Method/path | Behavior |
| --- | --- |
| `GET /config` | Explicit bindings and count of unacknowledged relay rows |
| `PUT /config` | Audited replacement: `{"bindings":[{"board":"plate-test","source_session_id":"session-one"}]}`; stopped input only |
| `POST /start` | Optional `{"port":"5085"}`; numeric event, nonempty bindings and unoccupied port required |
| `POST /stop` | Stop/join edge only; leave the event's site pull running if native ingest remains active |
| `GET /journal?after=0` | Read-only envelope export, at most 500 rows, `next_after` and `may_have_more` |

`GET /api/events/{id}/live/status` retains native fields and adds `edge` and
`any_running`. The header uses the combined state; the edge input adds no extra
frontend polling loop. Configuration, export and counter refresh are explicit.
`GET /api/version` advertises `edge_observation_version: 1` separately from the
unchanged event-export, native reader and v3 synchronization versions.

## Verification and release boundary

Use pinned Go 1.24.13 for Desk. Development currently requires a `go.work` with
this repository and the unpublished core feature tree (`f29ef24` or its reviewed
successor). The unchanged `go.mod` still points to released core v0.3.0, which
does not contain the new API; an immutable new module pin and `GOWORK=off` build
are required before a release. Development constants are not release provenance.

Checks: `go test ./...`, `go test -race ./...`, `go vet ./...`,
`staticcheck ./...`, `go build ./...`, frontend production build, `npm run smoke`
and `npm run smoke:edge`. The edge browser test drives the built Svelte screen
against an authenticated synthetic localhost API, verifies unsaved-binding and
active-edit guards and exact save/start/stop requests. It does not replace real
Go receiver tests or a hardware/handset run.

`CHRONO_DESK_BROWSER` may select an installed Chromium headless-shell binary.
The harness blocks external DNS/proxy traffic while exempting loopback; it never
forwards requests to the Internet. Here headless shell passes both smokes; the
installed full Chrome times out, so no full-Chrome/native-WebKit acceptance is
claimed. Vulnerability scans were not freshly accepted: public govulncheck egress
was denied, and npm dependencies were installed from the offline cache without
audit. These limitations are not a green `make quality` or a release waiver.

No source push, release tag, field installation or production observation is
part of this local checkpoint. Back up the event database before a future pilot;
an older binary does not forward the new relay journal. Preserve its database
for recovery rather than deleting edge tables to make a rollback appear clean.
