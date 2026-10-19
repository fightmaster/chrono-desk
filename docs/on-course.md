# On-course roster and operational counts

Task: CHR-DESK-UI-001
Docs-Impact: LOCAL

The results screen adds «На дистанции», a searchable per-race roster with bib,
name, category, start time and direct member-card access. The header separately
shows finished, expected, DNF, DNS and DSQ. It no longer implies that subtracting
finishers from the legacy started denominator gives expected finishers.

A non-reserve registration with status OK, no finish and an effective start at or
before now is expected. Individual start takes precedence over race mass start.
Mass start plus registration is an expectation, not evidence of physical passage:
no mark alone never assigns DNS. Future, missing or nonpositive starts are separate.
DNS/DNF/DSQ remain explicit judge states even when legacy times exist. Reserve
slots are outside the active start list. Incomplete finishes/unknown statuses are
labelled for review. For TimeLimited races a current ranked split does not imply
completion before the configured window ends; expired unclassified cases need
review rather than an invented DNF/finish.

`GET /api/events/{event}/races/{race}/protocol` adds `progress` with `counts` and
`on_course`. Existing `counts`, official rows/places, CSV and public projections
retain their previous semantics. Missing progress from an older API displays an
unavailable roster and legacy labelled counts, never a false zero.

The response shares the handler's coherent SQLite read snapshot. Members,
categories and ranking inputs are already loaded; one bulk event-scoped reserve
metadata SELECT is added, with no per-member SQL or timing writes. Classification
is linear plus sorting expected rows. While results are visible the existing
three-second header tick also requests protocol refresh, one active poll plus
at most one pending poll. Navigation cancels stale requests; race identity guards
prevent a late answer replacing another distance. It never controls app-owned
reader or camera ingestion. Exact added time/resource overhead remains a measured
release check, not an assumed zero cost.

Acceptance covers status partitioning, unknown/future/individual starts, manual
finish, active/foreign/deleted reserve metadata, TimeLimited timing, unchanged
official rows and empty-array serialization. Built-browser smoke covers search,
member-card opening, automatic roster refresh, empty races, rapid distance changes
and older API data. Run the complete final gate after finishing source changes.
