# Judge-local unfinished protocol appendix

Task: CHR-RESULT-001  
Docs-Impact: LOCAL

This is the small desktop-only delivery slice, based on release candidate
`e58da7a` / 0.4.3. Broader web/progress-state work and task acceptance remain in
`chrono-docs/TODO.md`; this document is implementation evidence, not another
backlog. No publication, tag, release or field installation is implied.

## Behavior

Open an event and distance, then select **Полный протокол**. After the normal
ranked/status rows, **Без финиша** lists entrants satisfying all three rules:

1. The saved member status is ordinary OK, not DNS/DNF/DSQ.
2. There is no saved finish timestamp.
3. The member is not already present in the format's ranked protocol.

The third condition preserves valid TimeLimited checkpoint/lap outcomes, which
do not require a FINISH checkpoint. Finished members with incomplete or invalid
timing basis are also not described as lacking a finish; their existing ranking
behavior is unchanged.

No readings, START-only and SPLIT-only participants all remain visible under
the cautious label **Финишная отметка пока не получена**. The appendix does not
assert who started or remains physically on course, infer DNS/DNF, or display a
fabricated place/time. The existing header tallies are unchanged; their
`started = total - DNS` convention is not new evidence of physical starting.

The same bib/name, gender and category controls filter both groups. Category
options include categories occurring only among unfinished entrants. Clicking
an appendix row opens the existing participant detail/judge drawer; existing
permission/token and edit/recount behavior remains intact. A protocol refresh
after a finish or explicit status removes the row from the appendix and shows
the existing normal result/status row. Refresh cadence is unchanged; this
feature adds no polling timer.

## Implementation boundary

- `BuildProtocol` appends separate `unfinished_rows` after ordinary ranking.
  `rows`, places, winners and header calculation are not modified.
- It reuses the existing member/category loads. No additional SQL or per-member
  checkpoint lookup is added. Appendix ordering is the existing member-query
  order, not a new sporting order.
- `ResultsScreen.svelte` renders only on its full-protocol tab. Shared filter
  functions receive all controls explicitly, fixing the previously hidden
  Svelte reactive dependencies while preserving matching rules.
- Public LAN `toPublicProtocol` and `BuildProtocolXLSX` use `rows` alone and do
  not publish/export the judge-local appendix.
- There are no saved statuses, schema changes, raw edits, engine changes,
  chip-time decisions or shared sync/export payload changes. Older local
  protocol responses lacking `unfinished_rows` render without an appendix.

## Automated and browser verification

`make quality` includes the existing formatting/vet/staticcheck/full-Go/race
checks and frontend build, plus three Node filter tests and the new
`npm run smoke:protocol` built-bundle interaction smoke. The latter uses the
real Svelte application, Wails bootstrap stubs and synthetic in-memory API
responses in cached headless Chromium; it never contacts a timing service.

Service/SQLite regression coverage includes no-read/start/split entrants,
finished and explicit special statuses, stale clean-time text, incomplete
finish timing, refresh after finish or each judge status, stable normal rows,
single member load, one bulk TimeLimited pass lookup, valid TimeLimited results
without FINISH, and exclusion from XLSX/public projection.

The browser scenario opens a synthetic event, switches awards/full protocol,
checks blank places/time and the cautious label, exercises bib/name/gender and
unfinished-only category filters, opens the actual participant drawer, marks a
synthetic DNF through its existing action, refreshes after a delayed finish,
uses the Excel action, and checks TimeLimited and older-response behavior.
Only asynchronous DOM readiness is awaited; no production packets are sent.

## Compatibility and acceptance limits

Local verification on 2026-09-08: `make quality` passed (172 Go test functions,
full race detector, vet/staticcheck/formatting, three Node tests, npm audit with
zero findings, Safari-14-targeted Vite build and both Chromium smokes).
`go build ./...` and native Linux/amd64 `make build` also passed with Go 1.24.13.
The separate `make audit` returned nonzero: 17 called vulnerability findings,
plus 10 imported-package and 29 required-module findings not reported as called.
These are from the unchanged Go/Excelize/x/net baseline, not remediated here.

The Go 1.24.13, Node 22, Safari/WebKit 14 target, Wails native version settings,
dependency pins and macOS 11 Intel deployment target remain unchanged. A Linux
build/browser check is not a native Big Sur acceptance: macOS Wails packaging
requires a Mac runner or the physical Mac and is not cross-compiled here.
Known vulnerability findings constrained by the existing Go/macOS baseline
remain a separate explicit `make audit` result; versions are not upgraded to
hide them.

For a physical judge acceptance, open a copied event DB containing a finisher,
no-read/start-only/split-only entrants and DNS/DNF/DSQ. Check filters and detail,
receive a delayed finish, refresh/recount, confirm that the entrant moves into
normal results, and compare awards/XLSX/LAN output with the previous version.
This slice does not yet add last-seen checkpoint/freshness evidence or the wider
web surface described in the central CHR-RESULT-001 audit.
