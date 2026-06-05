# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Offline-first Wails v2 desktop app (Go + Svelte) that substitutes the run5 site at
competition venues without internet: import an event export, ingest RFID logs (flash
drive CSV in v1, TCP later), recount results offline, show standings, export Excel
protocols. The site remains the source of truth — sync is site → desktop. Read
`docs/architecture.md` before structural changes; `docs/event-export-format.md` is the
import contract.

**Status: docs-first stage.** Scaffolding (wails init, go.mod `gitlab.com/fightmaster1/chrono-desk`)
is the next step; update this file as code lands.

## Toolchain & commands

- **Go pinned to 1.24** (`go 1.24` + `toolchain` in go.mod): Go 1.25+ requires macOS 12
  and won't run on the competition MacBook (mid-2014, Big Sur 11.7). Never bump the Go
  version without confirming the competition machine changed. Wails CLI v2.11, Node 22.
  SQLite via `modernc.org/sqlite` — **pure Go, keep CGO-free**; do not introduce
  CGO-dependent SQLite drivers.
- Dev machine is Ubuntu: requires `libwebkit2gtk-4.1-dev` and the `-tags webkit2_41`
  build flag. Target competition machine is an old MacBook; macOS builds happen in
  GitHub Actions or on the Mac itself (no darwin cross-compile from Linux).
- `wails dev -tags webkit2_41` — dev mode with hot reload;
  `wails build -tags webkit2_41` — production Linux binary.
- `go test ./...` — full suite; single test: `go test -run TestName ./path/to/pkg`.
- Quality gate before finishing any Go change (same as rfid-hub/rfid-sync): `gofmt`,
  `go vet ./...`, `staticcheck ./...`, `govulncheck ./...`, `go test ./...`; add
  `go test -race ./...` for concurrency-sensitive changes. If a check can't run in the
  current environment, say so explicitly instead of skipping silently.

## Architecture (planned layout)

`frontend/` (Svelte) → `internal/transport/httpapi` (embedded localhost REST; the UI
does NOT use Wails bindings — `app.go` exposes only `APIBaseURL()`, pattern from
RaceTorchApp) → `internal/service` (import/recount/ranking/excel) →
`internal/processor` (checkpoint-matching engine) → `internal/domain` →
`internal/infrastructure/sqlite`. One event = one portable SQLite file.

## Critical contracts

- **rfid_logs idempotency**: `id = md5(board + epc + timeMillis + ant)` concatenated
  without separators (board `"Feibot:U659"`, EPC uppercase/trimmed, decimal strings);
  number-based devices: `md5(board + number + timeMillis)`. Must stay byte-identical to
  `rfid-hub/internal/tcp/adapters.go` and run5 `app/Models/RfidLog.php`. Never change
  unilaterally.
- **Checkpoint semantics**: types 1=START/2=CHECKPOINT/3=FINISH; one result max per log —
  first eligible checkpoint by `sort` passing the since/since_offset/sleep/order guards;
  FINISH computes `clean_time` as `HH:MM:SS.mmm`. The reference implementation is
  rfid-sync's processor (see below) — keep behavior identical.
- **Disabled logs** (`disabled_at != null`) are imported but excluded from recounts
  (run5 ADR-0007).
- Times are unix milliseconds internally; Feibot flash CSV is zoneless local time —
  imports require an explicit timezone.

## Sibling projects (read before reinventing)

- `../rfid-sync/internal/syncer/processor/` — **the result-derivation engine this app
  ports** (Repository interface + processor.go + table-driven `processor_flow_test.go`).
  Ranking is NOT there (site-side); chrono-desk implements its own for display.
- `../rfid-hub/internal/tcp/` + `internal/ingest/` — TCP listeners, Feibot/MyRaceNano/
  ChronoEvents adapters, `Publisher` interface. Planned for v0.2 live ingest: copy
  (internal packages aren't importable cross-module), implement a SQLite `Publisher`.
- `~/projects/run5` — Laravel site; data model in `app/Models`, derivation reference in
  `app/Services/PushResult.php` + `RecountRfid.php`, protocol columns in
  `RaceResultsExportRowBuilder`. **The ranking system is ported from here** (format
  strategies in `app/Results/Format/`, place assignment in `app/Results/Services/`) —
  porting spec with exact semantics: `docs/ranking.md`. UI language is Russian,
  code/identifiers English.
- `~/GolandProjects/RaceTorchApp` — Wails v2 reference implementation (layout, embedded
  HTTP API, sqlite store, multi-platform build files).

## Conventions

- English code/comments/docs; Russian UI strings (run5 convention).
- Behavior/config/API/schema changes update the matching docs (`README.md`, `docs/*`)
  in the same change.
- Golden tests are the safety net for the recount engine: real exported events in
  `testdata/` with reference results; Go recount must reproduce them exactly.
- Small focused commits, imperative summaries (`Add Feibot CSV importer`).
