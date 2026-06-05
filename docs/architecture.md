# Architecture

## Goal

A multiplatform (macOS primary, Windows, Linux) desktop app that replaces the run5 server
in offline zones at competition sites: view results, top-3, print protocols, make
temporary registration fixes — without internet access.

## Non-goals (v1)

- Bidirectional sync / conflict resolution. The site is the source of truth; the desktop
  is a helper. Priority flow: site changes → re-export → re-import → recount.
- Ranking parity for every run5 race format. v1 covers FixedDistance-style ranking
  (sort by clean time); other formats follow.
- Replacing rfid-hub on site. v1 ingests logs from flash-drive CSV only; live TCP ingest
  arrives in v0.2.

## Layers

```
frontend/ (Svelte + Vite)  — UI, talks HTTP to localhost
   │
internal/transport/httpapi — REST API (embedded; later exposable to LAN / headless)
   │
internal/service           — import, recount orchestration, ranking, excel export
   │
internal/processor         — checkpoint-matching engine (port of rfid-sync processor)
   │
internal/domain            — Event, Race, Category, Checkpoint, Member, RfidLog, Result
   │
internal/infrastructure/sqlite — repositories, schema, migrations (modernc.org/sqlite)
```

Wails v2 hosts the frontend and the Go process in one binary (pattern proven in
RaceTorchApp: `app.go` exposes only `APIBaseURL()`; everything else goes over HTTP).

## Key decisions

1. **One event = one SQLite file** (e.g. `marathon-2026.chrono`). Portable by flash
   drive between laptops, backup = file copy, corruption is isolated per event.
2. **Reuse the rfid-sync derivation engine, not the Laravel one.** rfid-sync's
   `internal/syncer/processor` already implements member matching by EPC/number,
   checkpoint eligibility (since / since_offset_seconds / sleep_after_prev_seconds /
   sort order), result insertion and start/finish/clean-time updates — behind a clean
   `Repository` interface with table-driven tests. chrono-desk ports it (Go → Go) and
   backs it with SQLite instead of MySQL. Ranking/materialization is NOT in rfid-sync
   (the site does it); chrono-desk implements its own ranking for protocol display.
3. **HTTP API instead of Wails bindings.** Gives LAN access for judges' tablets later,
   headless mode for free, and keeps the frontend decoupled.
4. **Idempotency contract.** `rfid_logs.id = md5(board + epc + timeMillis + ant)`
   (string concatenation, no separators; board like `"Feibot:U659"`, EPC uppercased and
   trimmed, time as unix-millis decimal string, ant as decimal string). For number-based
   devices: `md5(board + number + timeMillis)`. Source of truth:
   `rfid-hub/internal/tcp/adapters.go` (`parseRFID`); run5 `app/Models/RfidLog.php` must
   match byte-for-byte. Logs collected by chrono-desk can therefore be uploaded to the
   site (and vice versa) without duplicates.
5. **Times are unix milliseconds everywhere internally.** Feibot flash-drive CSV carries
   local wall-clock time without zone — the import dialog requires an explicit event
   timezone (defaulted from the event export).
6. **Disabled logs are part of the contract.** run5 ADR-0007 soft-disables false reads
   (`disabled_at`). Exports include disabled logs; recount skips them.

## Data flow (v1)

```
run5 site ──(event export JSON)──▶ chrono-desk import ──▶ event .db file
Feibot reader ──(USB flash CSV)──▶ CSV import (dedup by id) ──▶ rfid_logs
                       recount: wipe derived results → replay rfid_logs
                       sorted by time through the processor → results,
                       member start/finish/clean times → ranking → UI / Excel
```

v0.2 adds: Feibot ──TCP──▶ chrono-desk listener (rfid-hub `internal/tcp` adapters with a
SQLite `Publisher`) ──▶ rfid_logs → incremental processing → live standings.

## Checkpoint semantics (inherited from run5 / rfid-sync)

- Types: 1=START, 2=CHECKPOINT, 3=FINISH. Checkpoints belong to a race, are bound to a
  physical reader via `board`, ordered by `sort`.
- A log produces at most one result: the first eligible checkpoint in sort order that is
  not yet passed, past its activation time (`since` or member start + `since_offset_seconds`),
  past the sleep window after the previous result, and with `sort` greater than the last
  passed checkpoint.
- START sets `member.start_time`; FINISH sets `member.finish_time` and computes
  `clean_time` (`HH:MM:SS.mmm`).

## Testing strategy

- Port rfid-sync's table-driven processor tests alongside the engine.
- **Golden tests against real events**: export a finished event from run5 (rfid_logs +
  reference results/member times) into `testdata/`; the Go recount must reproduce the
  reference exactly. This is the guard against PHP/Go divergence — protocols computed
  offline must match the site.
- Importers (event JSON, Feibot CSV) get fixture-based tests including timezone and
  dedup cases.
