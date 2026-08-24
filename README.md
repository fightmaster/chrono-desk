# chrono-desk

Offline-first desktop companion for the run5 race-timing platform. When there is no
internet at a competition site, chrono-desk acts as a local "server analogue": it imports
an event exported from the run5 site, ingests RFID logs (from a flash drive in v1, over
TCP from Feibot readers later), recalculates results offline, shows live standings and
top-3, and exports printable protocols to Excel.

The site stays the source of truth. Synchronization is bidirectional: the desktop pulls
the current event export and pushes local edits/manual finishes plus only the raw
observations recorded in its durable outbound journal. Push schema v3 uses a server
capability preflight and item-level acknowledgements, so observations pulled from other
timing points are never claimed or uploaded by this desktop. Manual pull imports the full
event snapshot and then drains the v1 change feed page by page, committing each opaque
cursor atomically with its observations. Starting live ingest also starts a five-second
background pull; stopping live ingest stops it. Manual and background pulls are serialized
per event.

## Ecosystem

| Project | Stack | Role | Local path |
|---|---|---|---|
| run5 | Laravel | Main site: registration, events, results, protocols | `~/projects/run5` |
| rfid-hub | Go | On-site TCP ingest from RFID readers → Redis Stream | `../rfid-hub` |
| rfid-sync | Go | Redis Stream → MySQL; derives results from rfid_logs | `../rfid-sync` |
| run5-stopwatch | Cordova | Offline stopwatch / manual finish-order capture | `~/projects/run5-stopwatch` |
| RaceTorchApp | Go + Wails | Finish-camera photo processing (future integration) | `~/GolandProjects/RaceTorchApp` |

chrono-desk and rfid-sync use the same immutable `timing-core` release for
checkpoint eligibility, member-time projection, outcome materialization and
ranking. Each application retains only its storage/transaction adapter. TCP
ingest uses the shared `rfid-core` module.

## Stack

- Go 1.24 (pinned: newer Go drops macOS 11, the competition MacBook's OS), Wails v2,
  Svelte + Vite frontend (same pattern as RaceTorchApp)
- SQLite via `modernc.org/sqlite` (pure Go, no CGO) — one event = one portable `.db` file
- Excel export via `excelize`
- UI talks to the Go core through an embedded localhost HTTP API (not Wails bindings),
  so the same API can later be opened to the local network and run headless

## Status & roadmap

v0.1 core works end-to-end: import a run5 event export (JSON) → import Feibot flash-drive
CSV per reader → offline recount (engine ported from rfid-sync, golden-tested against a
real 8.5k-log production event byte-for-byte) → ranked protocol (FixedDistance +
TimeLimited) with top-3, category podiums and Excel export matching the site's layout.
Remaining for v0.1: the run5 `event:export` command on the site.

- **v0.1 (MVP)**: import run5 event export (JSON) · import Feibot CSV from flash drive ·
  offline recalculation · results screen + top-3 · Excel protocol export
- **v0.2**: live TCP ingest from Feibot in the local network, live standings
- **v0.3**: push-own observation batches with durable acknowledgements · registration
  and judge edits sync · reader heartbeat monitoring · snapshot + incremental pull
- **v0.4**: shared `timing-core v0.8.0`, start provenance, concurrent pull-all /
  push-own evidence, revision/parity telemetry and source-addressed release artifacts
- later: RaceTorch integration, multi-tool monitoring/processing center; a machine-readable
  results feed (e.g. XML/JSON) for IPTV/streaming overlays so a broadcast can show live
  times and the leaderboard as on-screen text (idea — to be scoped)

## LAN results broadcast

At a venue with no internet the only copy of the live results is on the desk. People who
sign certificates, engrave medal times or run social media can read them from their own
phones: in **Настройки события → «Трансляция результатов по сети»** the operator turns the
broadcast on and shows an address + QR code. Anyone on the same Wi-Fi opens that page and
gets a read-only results board — a distance dropdown and two tabs like the desktop:
**Призёры** (absolute M/F top-3 plus age-group podiums) and **Протокол** (full table with
search by surname/number), auto-refreshing as the judge recounts. The Призёры tab has a
**copy-for-Telegram** button that puts the winners/prize-winners on the clipboard as
Markdown, so the SMM person posts without retyping names.

This runs as a **separate read-only HTTP server** (`internal/transport/publicweb`, default
port `8090`, override `CHRONO_PUBLIC_PORT`), distinct from the localhost control API: only
GET endpoints with a PII-trimmed projection are exposed (no date of birth, no internal
ids, no edits, no sync token). The LAN port is opened only while broadcasting and closed
when it is switched off — nothing is reachable from the network by default. Address
discovery skips virtual interfaces (Docker bridges, VMs, VPNs) so a QR never points at an
unreachable address; if the machine has several real NICs up (Ethernet + Wi-Fi), the
settings screen shows each address with its own QR so the operator picks the venue one.

The embedded localhost control API is separate and requires a random memory-only bearer
token passed from Go to the Wails frontend at startup. That token does not apply to the
read-only LAN server and is never included in its QR codes.

## Development

Always build through `make` — it pins `GOTOOLCHAIN=go1.24.13` (see docs/architecture.md)
and passes the `webkit2_41` build tag for Ubuntu 24.04. Frontend tooling requires
Node `^20.19.0 || >=22.12.0`; the release workflow uses Node 22 and local release
evidence uses Node 24.18.0:

```bash
make dev              # hot reload
make build            # production binary for the current OS
make test             # full test suite
make race             # race-detector pass
make check            # required gate: gofmt, test, race, vet, staticcheck
make quality          # clean-checkout gate: npm ci/audit/build, then make check
make audit            # vulnerability report (known Go 1.24 findings; see architecture)
```

`timing-core` is pinned to `v0.8.0` and canonical GitLab `rfid-core` to
`v0.2.0`; release builds never use mutable sibling replacements. Because both
GitLab projects are private, GitHub Actions requires one read-only group/deploy
token that can read both repositories in the `TIMING_MODULES_READ_TOKEN`
repository secret. `TIMING_CORE_READ_TOKEN` remains a temporary fallback only
when it already has access to both projects. The workflow fails before the
Wails build when neither token can download both tags.
Pushes to `release/**` run the same quality and macOS/Windows packaging jobs as
a tag, allowing credentials and platform builds to be rehearsed before an
immutable version is published. Only an exact `v$(cat VERSION)` tag creates the
GitHub Release.
Generated Wails JavaScript bindings are tracked because the frontend quality
gate must build from a clean checkout without launching Wails. GitHub Actions
builds the frontend first because `main.go` embeds `frontend/dist`, then runs
backend tests, race detection, formatting, vet and staticcheck before either
release artifact.
The version API exposes `member_time_version=member-time-v2-start-provenance`;
manual and unclassified starts remain protected while machine-owned starts
trace their race default or immutable observation.

Release `v0.4.0` stamps the full source commit and commit timestamp into both
the application diagnostics and projection-evidence identity. Tag builds create
versioned macOS 11 Intel and Windows artifacts, generate and verify
`SHA256SUMS`, and publish all files as an immutable GitHub release. The macOS
bundle is always verified with `codesign --deep --strict`: without signing
secrets it receives an explicit ad-hoc signature for direct competition use;
with `DEVELOPER_ID_APPLICATION`, `MACOS_CERTIFICATE_P12` and
`MACOS_CERTIFICATE_PASSWORD` it receives a timestamped Developer ID signature.
The release includes `chrono-desk-macos-signing.txt`, so these modes cannot be
confused. Notarization is not claimed.

`GET /api/version` now reports the complete timing-core identity plus event
export v3, sync push v3 and change-feed v1. The title bar remains compact, while
the endpoint is the machine-readable evidence used during field acceptance.

## Docs

- [docs/architecture.md](docs/architecture.md) — components, data flow, key decisions
- [docs/event-export-format.md](docs/event-export-format.md) — the run5 → chrono-desk
  event export contract (JSON, schema_version 3)
