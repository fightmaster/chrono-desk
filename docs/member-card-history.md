# Member card history loading

Task: CHR-DESK-PERF-001
Docs-Impact: LOCAL

The drawer initially requests accepted checkpoint passes only. Registration fields and
the small pass list arrive in one coherent read snapshot (`member` in the pass
response). A separate registration GET remains a compatibility fallback for old APIs. Disabled and unaccepted readings are
fetched when the operator clears **«только засчитанные»**. Loaded history is reused
when toggling the filter; a member reload invalidates it. Closing/reloading the
drawer aborts outstanding reads and ignores stale responses even if transport
cancellation races with completion. Manual judge results are present in both
responses, including the earliest-manual arbitration inputs.

The old initial filter also included disabled rows. On event772963 the largest
card received1010readings and rendered506rows,505of them disabled. The new initial
view receives and renders only its one accepted pass. History and disable flags
remain in storage and are available through the explicit full-history view.
No recount, correction, schema, identity, ownership or sync behavior changes here.

## Local control API

`GET /api/events/{event}/members/{member}/passes`

| Query | Response |
| --- | --- |
| omitted / `scope=all` | Existing complete raw-tag history and manual results; backward compatible |
| `scope=accepted` | Only rows with a checkpoint result for this member and no `disabled_at`; same member/tag association as the existing view, plus the same manual results |
| other `scope` | HTTP400 |

The JSON shape remains `MemberPasses`. No raw-history count is loaded for the
initial view: the header shows the accepted count, then the full count after the
operator requests history. An empty accepted response does not claim the tag has
no raw readings. The full view deliberately retains the existing semantics.

The accepted query starts with `results(member_id)` and looks up each RFID row
by its primary key. `CROSS JOIN` fixes that order for this bounded view; on files
without ANALYZE statistics a regular join was reordered to scan the whole event.
This follows SQLite's documented [join-order control](https://sqlite.org/optoverview.html#manual_control_of_query_plans_using_cross_join).
An EXPLAIN regression test guards against restoring the event-wide scan.

## Verification

HTTP tests cover active accepted, unaccepted, disabled-with-stale-result,
another member's result, manual facts, the legacy default, invalid scope and
unchanged raw/disabled/member records. JS tests cover cancellation and late
responses. The real frontend/API browser probe checks accepted-only startup,
explicit history loading, filter caching and reopen behavior.

On the sanitized772963copy,15Linux headless Chromium opens: median click-to-two-RAF
after fields70.6ms, p9581.6ms, max163.2ms, compared with483.6/641.3/682.3ms in the
earlier all-history run. These are single-run diagnostic figures on a loaded Linux
host, not a Mac WebKit guarantee or a physical paint trace. Initial1row expands
to1010only on request; the repeat filter makes no extra history request. No app
installation or working-event mutation occurred. See the
[performance audit](reports/chrono-desk-performance-2026-10-19.md) for the wider
read-pool and batch-engine work, which this optimization does not replace.

Validation: full Go tests/race/vet/build, staticcheck, JS request tests, Vite
production build, runtime/edge browser smoke and the lazy-history browser scenario
pass. Staticcheck used an identical private source copy with the exact local
rfid-core v0.4.2 source replacement; that card-only checkpoint left module files unchanged. The subsequent shared
snapshot candidate pins timing-core as documented in [snapshot recount](snapshot-recount.md).
The Go1.24.13 vulnerability audit reports existing reachable standard-library
findings; it is not a clean audit/release acceptance. Compatibility and dependency
remediation remain centrally tracked by CHR-SEC-002. No vulnerability remediation is included in this performance candidate.
