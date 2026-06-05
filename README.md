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

Docs-first stage; application scaffolding is next.

- **v0.1 (MVP)**: import run5 event export (JSON) · import Feibot CSV from flash drive ·
  offline recalculation · results screen + top-3 · Excel protocol export
- **v0.2**: live TCP ingest from Feibot in the local network, live standings
- **v0.3**: upload collected rfid_logs back to the site · registration edits sync ·
  reader heartbeat monitoring
- later: RaceTorch integration, multi-tool monitoring/processing center

## Development

Quality gate (same as rfid-hub / rfid-sync):

```bash
gofmt -l .            # formatting
go vet ./...
staticcheck ./...
govulncheck ./...
go test ./...         # single test: go test -run TestName ./path/to/pkg
go test -race ./...   # required for concurrency-sensitive changes
```

App commands (once scaffolded):

```bash
wails dev             # dev mode with hot reload
wails build           # production binary for the current OS
```

## Docs

- [docs/architecture.md](docs/architecture.md) — components, data flow, key decisions
- [docs/event-export-format.md](docs/event-export-format.md) — the run5 → chrono-desk
  event export contract (JSON, schema_version 1)
