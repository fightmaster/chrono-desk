# chrono-desk

Offline-first desktop companion for the run5 race-timing platform. When there is no
internet at a competition site, chrono-desk acts as a local "server analogue": it imports
an event exported from the run5 site, ingests RFID logs (from a flash drive in v1, over
TCP from Feibot readers later), recalculates results offline, shows live standings and
top-3, and exports printable protocols to Excel.

The site stays the source of truth. Sync is one-directional: site → desktop via
export/import. When connectivity returns, changes are made on the site and the event is
re-exported; rfid_logs collected offline are uploaded back (they are fully idempotent).

## Ecosystem

| Project | Stack | Role | Local path |
|---|---|---|---|
| run5 | Laravel | Main site: registration, events, results, protocols | `~/projects/run5` |
| rfid-hub | Go | On-site TCP ingest from RFID readers → Redis Stream | `../rfid-hub` |
| rfid-sync | Go | Redis Stream → MySQL; derives results from rfid_logs | `../rfid-sync` |
| run5-stopwatch | Cordova | Offline stopwatch / manual finish-order capture | `~/projects/run5-stopwatch` |
| RaceTorchApp | Go + Wails | Finish-camera photo processing (future integration) | `~/GolandProjects/RaceTorchApp` |

chrono-desk reuses the result-derivation algorithm from rfid-sync
(`internal/syncer/processor`) and, later, the TCP listener + Feibot adapter from rfid-hub
(`internal/tcp`, `internal/ingest`).

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
- **v0.3**: upload collected rfid_logs back to the site · registration edits sync ·
  reader heartbeat monitoring
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
and passes the `webkit2_41` build tag for Ubuntu 24.04:

```bash
make dev              # hot reload
make build            # production binary for the current OS
make test             # full test suite
make race             # race-detector pass
make check            # full quality gate: gofmt, vet, staticcheck, govulncheck, test
```

## Docs

- [docs/architecture.md](docs/architecture.md) — components, data flow, key decisions
- [docs/event-export-format.md](docs/event-export-format.md) — the run5 → chrono-desk
  event export contract (JSON, schema_version 3)
