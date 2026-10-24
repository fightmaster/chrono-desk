# Live finishes for the presenter

Task: CHR-DESK-UI-002. Docs-Impact: LOCAL.

Enable **Трансляцию результатов по сети** in event settings. Open the
**Для ведущего · Live-финиши** link/QR on a tablet on the same Wi-Fi.
The address is `http://<desk-address>:8090/live` (`CHRONO_PUBLIC_PORT` can
override the port). The existing broadcast page also has a **Финиши** tab.
No Internet connection or tablet account is required.

Cards show bib, surname, first name, Club, Team, city, race, official time,
gender place and category place/name. Club and Team are separate affiliations.
Empty affiliations/city and unavailable places are omitted. Category places follow
the protocol policy, including exclusion of the gender top three where configured. All distances are selected initially;
the dropdown can select one. The latest 10 confirmed finishes appear in
absolute finish timestamp order, newest first, rather than elapsed-time order.
One person registered in two races has two participations. Bib alone is not
an identity key.

Repeated RFID reads cannot create cards: the source is committed participation
state, not individual observations. Corrections update the same card and revoked
finishes disappear. Only FixedDistance, OK status, nonempty official time and a
positive finish timestamp qualify. DNS/DNF/DSQ, reserves, intermediate passages
and TimeLimited races are excluded. Existing processing/recount rules remain
unchanged; the presenter page does not initiate them.

New cards briefly highlight. Manual scrolling retains the visible card's position;
**К новым ↑** returns to the top. Initial history, filter changes and reconnection
do not highlight old cards as new. There is no shared "announced" state.

An active Live view uses one SSE connection. A committed finish change triggers
an immediate current snapshot of the latest ten, with no500ms batching delay.
Busy builds/sends retain only the newest pending state. Idle streams send a tiny
heartbeat every five seconds and perform no periodic snapshot/database reads.
Heartbeat receipt updates connection liveness without rendering cards or changing
the data timestamp. Cards with unchanged content retain their existing DOM.

**Обновить**, returning from Protocol, changing distance, returning to a visible
tab and reconnecting all read a fresh committed snapshot. Club/name edits can
appear on the next finish or explicit refresh. Hidden/inactive Live views close
their stream and cancel pending requests/retries. Network failure keeps the last
snapshot with a stale-data warning. EventSource owns recoverable reconnects;
permanent HTTP errors reload JSON so an obsolete filter can reset. A15-second
silent-connection watchdog closes the old source before requesting fresh data.
Older servers/browsers retain the visible-only500ms poller with1/2/5s failure
backoff. Desk and observation processing continue independently of view navigation.

## Implementation

Task: CHR-SSE-001. Docs-Impact: LOCAL. Implemented in source; deployment is a
separate release action. [Design](plans/presenter-sse.md),
[ADR](adr/0001-presenter-sse.md).

`GET /api/live/finishes?race_id=<optional>` returns a bounded snapshot of only the
broadcast event: `published`, `filtered`, opaque `event_key`, event name/date,
FixedDistance `races`, at most10 `rows`, and additive `stream_version:1`. Empty lists are `[]`. No broadcast
returns `published:false` and empty rows; an unknown/unsupported distance returns
400; temporary failure returns 503. `/live.js` owns the feed lifecycle.

`GET /api/live/finishes/stream?race_id=<optional>` provides UTF-8
`text/event-stream` over the existing HTTP/1.1 LAN listener. Each connection gets
a newly read `event: snapshot` with the same public JSON DTO and an opaque `id`.
`event: heartbeat` carries `{}` and no revision. Unknown/old `Last-Event-ID` always
receives a fresh snapshot; this is not a durable replay queue. Stream IDs identify
publication/view revisions, never physical RFID timestamps. Invalid filters and
non-GET methods fail before streaming. Admission is capped at32 clients; excess
connections receive503 and retry guidance. JSON reads remain available.

One store-lifetime signal increments a generation and performs a capacity-one
nonblocking wake-up only after the outer projection transaction commits. Rollback,
failed commit, raw-only input and duplicate/intermediate reads do not emit a
finish change. See [writer inventory and lifecycle](presenter-sse-internals.md).
The dispatcher builds one immutable shared frame per active event/distance view.
Each client has at most one replaceable pending frame plus its in-flight send.
Four build slots bound reads; snapshot reads have a one-second deadline and
release all DB leases before encoding/network writes. Frame JSON is capped at
256KiB; oversize/error closes the stream instead of silently truncating data.

Each frame write/flush has a fresh one-second deadline. Slow clients cannot hold
the producer, dispatcher or another client's socket. Cancellation releases the
subscriber, worker, frame references and ticker. Publication changes invalidate
the epoch; unpublish/shutdown cancels active streams before HTTP shutdown waits.
There is no new dependency, DB migration, HTTP/2 requirement or shared timing/
export/sync version.

Each row contains `key`, `race_id`, `race_name`, `number`, `first_name`, `last_name`,
`club`, `team`, `city`, `finish_time_ms`, `clean_time`, `gender`, `gender_place`,
`category_name`, `category_place`. Participation keys use HMAC-SHA256
over event/registration identity with a random process key, stable until restart.
No raw member IDs, DOB, EPC, contacts or tokens are exposed. Access follows the
existing operator-enabled public read-only LAN broadcast. Settings add
`presenter_url`/`presenter_qr` alongside existing `url`/`qr` fields.

EventService reads event, races and finishes in one read-only transaction on the
independent SQLite/WAL reader pool. No BuildProtocol or replay. Shared `ranking.Protocol` runs only on a race-cache
miss, not for every client or heartbeat. Its immutable maps are shared by presenter clients and
keyed by database identity, config revision and clean-time eligibility revision.
The eligibility side counter covers null transitions omitted from the existing
configuration fence; raw observation traffic does not invalidate rank maps.
An older pinned snapshot cannot evict a newer cache. All reads remain coherent
and query-only, including the version tokens.
Two additive partial indexes on `members` support ordering by
`event_id[,race_id],finish_time_ms DESC,id DESC`. Migration is idempotent for old
files. Event-export, observation and site-sync contracts are unchanged.

## Validation

Run the ordinary gate with `make quality`. HTTP/SQLite tests cover bounds,
ordering, opaque keys, exclusions, corrections, revocation, reading while a
writer transaction is open, and index migration.

The opt-in browser integration test needs Node and Chromium:

```sh
CHRONO_PRESENTER_BROWSER=/path/to/chromium \
  go test ./internal/transport/publicweb -run TestPresenterBrowser -count=1 -v
```

It uses temporary event files and a loopback fixture, three independent Chromium
1024×768 clients,100 synthetic observation inserts/second and100 new finishes.
There are300 commit-to-DOM-plus-two-animation-frame samples; the existing p95
regression guard is<=1000ms and the lower250ms target is reported separately.
Capture-to-DOM is recorded separately. This measures DOM/paint opportunity on the
runner, not a physical tablet or Wi-Fi radio. It covers SSE idle/no card redraw,
force Club refresh, stale responses/callbacks, no parallel sources, filters,
scrolling, hidden/inactive cleanup, offline/reconnect, event switch, old-server
fallback, malformed frames and the real15s silent-connection watchdog. Fixture
mutation routes exist only in `_test.go`. Set
`CHRONO_PRESENTER_SCREENSHOT=/tmp/presenter.png` for a tablet screenshot.

Ordinary Go tests include two actual HTTP clients, a blocked writer and a real
nonreading TCP socket, actual native/sidecar TCP processing and ACKs, ownership
and duplicate checks, projection fault injection, outer commit/rollback boundaries,
1000 real HTTP reconnects,1000 hub reconnects, initial-build cancellation,
admission and shutdown. The HTTP resource test records post-GC heap deltas and
checks owned worker/group/subscriber counts, DB leases, goroutines and Linux FDs.
Use `CHRONO_PRESENTER_PROFILE_DIR=/tmp/presenter-profiles` for test-only heap and
goroutine profiles. No public profiling route exists.

Run the optional measurements separately from heavy quality work:

```sh
CHRONO_PRESENTER_PERF=1 go test ./internal/transport/publicweb \
  -run TestFinishStreamMatchedIngestPerformance -count=1 -v
CHRONO_PRESENTER_SOAK=30m CHRONO_PRESENTER_SOAK_DIR=/tmp/presenter-soak \
  go test ./internal/transport/publicweb -run TestFinishStreamSoak \
  -count=1 -v -timeout=35m
```

The matched benchmark records three repetitions each with SSE off/on, two normal
clients and one nonreading client, using the actual mixed native/sidecar ACK path.
The30m soak uses4096 fixed registrations, targeted100 observations/second,
approximately one finish/second and bursts, two draining clients and a repeatedly
reconnected nonreading client. Its initial30s raw-only interval must not build
snapshots. It checks exact persisted-input counts, final-client freshness, unique
latest-ten rows, reader leases and a frozen retained-heap budget; profiles are
written each minute. Shorter durations rehearse the harness and are not release
soak acceptance. Numeric budgets are fixed before their measurement; finite
runs provide regression evidence rather than proof of every possible leak.

Packaged Mac/tablet Wi-Fi measurements remain separate when devices and release
scope are available. The owner's previously waived general Desk field acceptance
is not reinstated. Go1.24/macOS11 support and existing CHR-SEC-002 findings remain.



## Club import and refresh

The source spreadsheet has distinct `Клуб` and `Команда` columns. Club is optional
display metadata in `member_club_metadata`, not a packet Person or ranking input.
RUN5 imports Club separately and supplies optional `members[].club` in its existing
event export. See the coordinated `chrono-docs/contracts/registration-display-metadata.md`.
Absent means retain previous metadata; explicit null clears a known club. Old
clients/export payloads remain compatible, including with packet issuance active.
JSON backups preserve Club values and explicit clears.

In event broadcast settings, **Обновить клубы с сайта** fetches the configured
site export using the existing scoped sync token, then updates only Club metadata
for existing matching event/member IDs. It never creates registrations, advances
packet/change-feed cursors, writes timing facts or triggers recount. The private
control route is `POST /api/events/{id}/presenter/clubs/refresh` behind the existing
control bearer. It is not reachable through the public read-only LAN listener.
There is no periodic site fetch from tablets. Refresh before the start and after
site roster additions if club information has changed. The public feed reads Club
metadata from the same committed snapshot and retains the ten-row limit.

Missing clubs previously discarded during import require a separately reviewed,
identity-matched restoration from the authoritative source file. Updating the app
alone cannot reconstruct those original values.


Current-main compatibility (CHR-REG-001): presenter ranks use the selected gun/chip
basis from the shared ranker. Derived chip-start corrections advance only the
presenter eligibility revision, so cached gender/category places refresh without
creating projection input work. Club changes still leave rank revisions alone.
Regression coverage combines Club schema5 export/import with distinct assigned
and measured starts and checks rank refresh after a chip-start correction.

### Club editing and refresh counts — CHR-REG-001

The member drawer loads its registration from the bundled accepted-passes response.
Both accepted/all pass views now include `member.club_person_id`, captured with
the registration in the same read snapshot. The standalone member endpoint keeps
the same field. Fresh-card edits echo that owner successfully; a real replacement
still rejects the stale card until reopened. A missing bundled owner previously
caused a false owner-changed error even when no participant had moved.

Club refresh reports `provided` (received Club metadata, including explicit null),
`updated` (changed stored metadata rows) and `populated` (current nonempty Club
values for the local event). The UI shows received and nonempty counts separately.
A508-row response containing507nulls represents one populated Club. Unknown IDs
never contribute a local populated count; explicit clears reduce it and absent
fields preserve known metadata. Count readback shares the update transaction.
