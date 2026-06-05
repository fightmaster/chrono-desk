# Repository Guidelines

## Project Structure & Module Organization

- Wails v2 app: `main.go` + `app.go` at the root, Go packages under `internal/`
  (`domain`, `processor`, `service`, `infrastructure/sqlite`, `transport/httpapi`),
  Svelte UI under `frontend/`.
- Architecture and contracts live in `docs/`; `docs/event-export-format.md` defines the
  run5 import contract — keep code, docs, and fixtures in sync when fields change.
- One event = one SQLite file; schema and migrations live in
  `internal/infrastructure/sqlite` and are embedded via `go:embed`.

## Build, Test, and Development Commands

- `wails dev` — run the app with hot reload.
- `wails build` — production binary for the current OS.
- `go test ./...` — full test suite; `go test -run TestName ./path/to/pkg` for one test.
- `cd frontend && npm install && npm run build` — frontend only.

## Coding Style & Naming Conventions

- Standard Go formatting (`gofmt`), tabs, exported `CamelCase` / local `camelCase`.
- English code, comments, and docs; Russian UI strings.
- Pure-Go dependencies only where possible; SQLite stays `modernc.org/sqlite` (no CGO).
- Keep persistence in `internal/infrastructure/sqlite`, business rules in
  `internal/processor` / `internal/service`, HTTP in `internal/transport/httpapi`.

## Testing Guidelines

- `_test.go` next to the code, Go `testing` package, table-driven where it fits.
- The recount engine mirrors rfid-sync's processor: port its table-driven flow tests and
  keep behavior identical.
- Golden tests: real run5 event exports + reference results in `testdata/`; recount must
  reproduce the reference exactly. Add a golden fixture when touching derivation logic.

## Agent Workflow Expectations

- After changing Go code, run `gofmt` on touched files and `go test ./...` at minimum.
- For dependency, build, concurrency, networking, protocol, or schema changes also run
  `go vet ./...`, `staticcheck ./...`, `govulncheck ./...`, `go test -race ./...`,
  `go build ./...`.
- If a check cannot run in the current environment, call it out explicitly.
- Update affected docs (`README.md`, `docs/*`, this file) in the same task as the
  behavior change.

## Commit & Pull Request Guidelines

- Small, focused commits with imperative summaries (`Add event import service`).
- PRs: short description, key changes, schema/config updates, manual test steps or
  screenshots for UI changes.
