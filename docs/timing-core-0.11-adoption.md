# Shared timing-core 0.11 adoption

Task: CHR-CUT-002
Docs-Impact: CROSS_PROJECT

## Scope

This update pins published timing-core v0.11.0, source
`65793ffc88f3466b875293af58c04996d2b55bfc`, from the Desk v0.6.13 baseline.
It aligns the library with the central Go replacement without changing the
SQLite schema, event export, sync wire contract, UI or supported race formats.
Go 1.24.13 and the macOS 11 / Safari 14 compatibility floor are retained.
A dependency update alone is not a new desktop release or a field installation.

## Actual execution paths

- Full/race fixed-distance recount still calls `ComputeOrderedFixedDistance`,
  the strict v1 API. Its ordered cursor and batched SQLite materialization stay
  inside the existing atomic transaction.
- Planned participant replay already exists in `Recounter.recountMembers`: it
  reads the selected participants' history and uses the shared per-reading
  processor. This update does not introduce participant replay for the first time.
- Native reception retains the existing direct participant processing path.
  Site pull retains its impact planner, evidence fence and fallback behavior.
- Time-limited races retain their established processor/ranking path. Desk does
  not gain RUN5 Stopwatch support by importing a newer core module.

## What the newer API does not activate automatically

Core 0.11 adds `ReplayMembersOrdered`, used by the new central publisher. It needs
unchanged full configuration, a complete compatible previous projection, scoped
ordered history and a consumer-owned publication fence. Desk currently stores
its derived SQLite state through a different adapter rather than that complete
projection baseline. Substituting the new call without adapting these boundaries
would be an algorithm/storage rewrite, not a safe dependency update.

The new merged-projection API is therefore not enabled in this change. There is
no claimed UI speedup, changed recount frequency, shorter SQLite writer lock,
or fix for the separately tracked native raw-commit/projection-failure recovery
issue (CHR-DESK-REC-001). Existing manual decisions, disabled observations,
producer ownership, opaque IDs and outbox behavior must remain intact.

## Validation

Passed locally on Go 1.24.13 with `GOWORK=off`: full `go test ./...`, full
`go test -race ./...`, `go vet ./...`, `staticcheck ./...` and `go build ./...`.
The suites include the golden event 621632 fixture (8474 raw rows), snapshot vs.
existing processor parity, selected-member replay/evidence, native/local HTTP
transport and v3 push/v1 pull compatibility tests. Opt-in tagged cross-project
Edge/MySQL integration was not rerun; no production upload was performed.

Fresh `npm ci`, high-severity audit (zero findings), production build, eight unit
tests, four smoke-harness tests and real Chromium bootstrap, Edge controls and
on-course roster smoke checks passed after the minimal Vite 8.2.4 audit fix.
Native macOS/Windows artifacts and installation are separate release stages.
Canonical adoption and release evidence belong in chrono-docs under CHR-CUT-002.

The 2026-10-21 vulnerability audit reports 25 reachable standard-library findings
on Go 1.24.13, plus 6 imported-package and 37 module findings without vulnerable
calls. An unchanged v0.6.13 baseline audited with the same current database has
the identical findings; the timing-core update introduces no additional reported
finding. This is recorded CHR-SEC-002 debt, not a clean security audit.
