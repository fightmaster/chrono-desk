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

Polling occurs 500 ms after the previous request completes, one request at a time.
Changing the filter cancels the old request and ignores obsolete responses.
Hidden tabs pause polling. Network failure retains the last snapshot with an
explicit warning and last successful update age. Retry intervals are 1/2/5 seconds;
returning to the tab or coming online immediately requests a fresh snapshot.
A warning means displayed data may be stale. Desk and its observation processing
must remain running. Stopping the broadcast closes its LAN listener.

## Implementation

Planned transport change (CHR-SSE-001): replace active-view polling with SSE
snapshots, immediate post-commit notification and bounded client backpressure.
The runtime described below still uses polling. See the
[implementation/test plan](plans/presenter-sse.md) and
[transport decision](adr/0001-presenter-sse.md).

`GET /api/live/finishes?race_id=<optional>` returns a bounded snapshot of only the
broadcast event: `published`, `filtered`, opaque `event_key`, event name/date,
FixedDistance `races`, and at most 10 `rows`. Empty lists are `[]`. No broadcast
returns `published:false` and empty rows; an unknown/unsupported distance returns
400; temporary failure returns 503. `/live.js` owns the feed lifecycle.

Each row contains `key`, `race_id`, `race_name`, `number`, `first_name`, `last_name`,
`club`, `team`, `city`, `finish_time_ms`, `clean_time`, `gender`, `gender_place`,
`category_name`, `category_place`. Participation keys use HMAC-SHA256
over event/registration identity with a random process key, stable until restart.
No raw member IDs, DOB, EPC, contacts or tokens are exposed. Access follows the
existing operator-enabled public read-only LAN broadcast. Settings add
`presenter_url`/`presenter_qr` alongside existing `url`/`qr` fields.

EventService reads event, races and finishes in one read-only transaction on the
independent SQLite/WAL reader pool. No BuildProtocol or replay. Shared `ranking.Protocol` runs only on a race-cache
miss, not on each poll. Its immutable maps are shared by presenter clients and
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

It uses temporary event files and a loopback fixture, three separate Chromium
1024×768 clients, 100 synthetic observation inserts/second, and 12 new finishes.
Its 36 latency samples run from sending the synthetic finish to DOM visibility
plus two requestAnimationFrame callbacks; p95 must be <=1000 ms. This measures
DOM and paint opportunity, not a physical screen or radio network. It checks
deduplication, correction, revocation, delayed obsolete filter replies, event
switching, scroll retention, hidden-tab pause, offline/reconnect, one request
in flight, and absence of full-protocol requests. Fixture POST routes exist only
in `_test.go`, never in the shipped application. Set
`CHRONO_PRESENTER_SCREENSHOT=/tmp/presenter.png` to retain a tablet screenshot.

Field acceptance remains separate: the competition MacBook, three actual tablets
on venue Wi-Fi and live input. Measure delivery to Desk separately from card
appearance. Check long names, filter changes, screen sleep, network interruption
and duplicate suppression. Local synthetic acceptance does not replace this.


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
