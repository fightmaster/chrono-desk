# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Offline-first Wails v2 desktop app (Go + Svelte) that substitutes the run5 site at
competition venues without internet: import an event export, ingest RFID logs (flash
drive CSV in v1, TCP later), recount results offline, show standings, export Excel
protocols. The site remains the source of truth — sync is site → desktop. Read
`docs/architecture.md` before structural changes; `docs/event-export-format.md` is the
import contract.

**Status: v0.1 core works end-to-end.** Implemented: processor port from rfid-sync
(`internal/processor` + flow tests), recount service, event-export JSON importer,
Feibot CSV importer (byte-identical md5 ids), FixedDistance ranking with both category
strategies, REST API (import/recount/races/protocol) and the Svelte results UI.
Next: Excel protocol export, golden tests on a real event from the local run5 DB,
TimeLimited/Run5Stopwatch formats, run5 `event:export` artisan command (site side),
v0.2 live TCP ingest. rfid-sync is implementing member_results materialization — when
it lands, diff its semantics against `internal/ranking`. Update this file as code lands.

## Toolchain & commands

- **Go pinned to 1.24** via `GOTOOLCHAIN=go1.24.13` in the Makefile (a go.mod
  `toolchain` directive can't downgrade, so always build/test through `make`): Go 1.25+
  requires macOS 12 and won't run on the competition MacBook (mid-2014, Big Sur 11.7).
  Never bump the Go version without confirming the competition machine changed.
  Consequences (accepted, do not "fix"): `govulncheck` reports stdlib vulns fixed only
  in 1.25+; `modernc.org/sqlite` is capped at v1.44.0. Wails CLI v2.11, Node 22.
  SQLite via `modernc.org/sqlite` — **pure Go, keep CGO-free**; do not introduce
  CGO-dependent SQLite drivers.
- Dev machine is Ubuntu: requires `libwebkit2gtk-4.1-dev` and the `-tags webkit2_41`
  build flag. Target competition machine is an old MacBook; macOS builds happen in
  GitHub Actions or on the Mac itself (no darwin cross-compile from Linux).
- `make dev` — hot reload; `make build` — production Linux binary (both pass
  `-tags webkit2_41` and the toolchain pin).
- `make test` — full suite; single test: `GOTOOLCHAIN=go1.24.13 go test -run TestName ./path/to/pkg`.
- Quality gate before finishing any Go change (same as rfid-hub/rfid-sync): `make check`
  (= gofmt, vet, staticcheck, govulncheck, test); add `make race` for
  concurrency-sensitive changes. govulncheck's stdlib findings are the accepted Go-pin
  consequence — report them, don't chase them. If a check can't run in the current
  environment, say so explicitly instead of skipping silently.

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
