# Event export format (run5 → chrono-desk)

Contract for the run5 artisan command `event:export {event_id}` (to be implemented on the
site) and the chrono-desk importer. One JSON document per event.

Re-import is an upsert by `id` (all ids are strings); importing the same export twice is
a no-op, importing a newer export overwrites site-owned data and triggers a recount.

```json
{
  "schema_version": 3,
  "exported_at": "2026-06-05T12:00:00+03:00",
  "timezone": "Europe/Moscow",
  "event": {},
  "laps": [],
  "races": [],
  "categories": [],
  "category_races": [],
  "checkpoints": [],
  "members": [],
  "rfid_logs": []
}
```

**Schema versions.** v1: `categories` carried only the groups referenced by the
event's members, and there was no `category_races`. v2: `categories` is the **full
global catalog** (so a judge can attach a not-yet-used group offline), plus a
`category_races` pivot mapping which categories are attached to which race. v3:
`event.use_race_date_for_age` tells the desktop whether age groups use only the
birth year (legacy default) or the full birth date on the race date. The importer
accepts v1/v2/v3; a v1 export (no `category_races`) seeds the pivot from
`member.category_id` so per-race chips don't regress.

`timezone` — IANA zone of the event; used as the default when importing Feibot
flash-drive CSV (which carries zoneless local time).

## Entities

Field sources: run5 `app/Models/*` and `database/migrations/*`. Datetimes are ISO 8601
strings with offset unless noted; `rfid_logs.time` is unix milliseconds.

### event

`id`, `name`, `slug`, `date`, `use_race_date_for_age`

`use_race_date_for_age` — when `false`, age categories use `race_year - birth_year`
(legacy run5 behavior). When `true`, age is calculated on the race date with the
full birth date.

### laps

`id`, `name`, `slug`, `description`

### races

`id`, `event_id`, `name`, `date`, `started_at`, `lap_id`, `format`
(`FixedDistance` | `TimeLimited` | `Run5Stopwatch`), `time_limit_seconds`,
`category_excludes_top_by_gender`

### categories

`id`, `name`, `min`, `max`, `gender`

The **full global catalog** (run5 categories are event-global, attached to races via
the `category_race` pivot). v1 exports carried only the used subset — see schema
versions above.

### category_races

`race_id`, `category_id` — run5's `category_race` pivot: which catalog categories are
attached to each race. This is the set the desktop offers when assigning a participant
(not the whole catalog). Re-import REPLACES the event's pivot (site is the source of
truth); local attach/detach edits (`race_category` journal entries) replay on top.
Protocol grouping still derives from members (`member.category_id`), mirroring run5
`Race::categoriesUsedByMembers()` — the pivot governs availability, not ranking.

### checkpoints

`id`, `event_id`, `race_id`, `name`, `type` (1=START, 2=CHECKPOINT, 3=FINISH), `sort`,
`board`, `since`, `since_offset_seconds`, `sleep_after_prev_seconds`

Required for any recount — a log is matched to a checkpoint by `board` and the
eligibility guards. **An export without checkpoints cannot be recounted.**

### members

Core: `id`, `event_id`, `race_id`, `category_id`, `number` (bib), `epc`, `rfid`,
`first_name`, `last_name`, `gender`, `dob`, `city`, `team`, `status`
(MemberStatus enum: ok/DNS/DNF/DSQ)

`gender` is exported as the string `"male"` / `"female"` / `null` — run5 stores an int
(1=male, 2=female), the `event:export` command must map it. `dob` is an ISO date
(`YYYY-MM-DD`).

Times (informational; chrono-desk recomputes them on recount): `start_time`,
`finish_time`, `clean_time`

Optional (for on-site registration fixes): `phone`, `sosPhone`, `email`,
`first_name_lat`, `last_name_lat`

### rfid_logs

`id`, `event_id`, `status`, `number`, `time` (unix millis), `ant`, `epc`, `rssi`,
`board`, `disabled_at` (nullable)

- `id = md5(board + epc + timeMillis + ant)` — see architecture.md, decision 4. The
  export must carry the stored id verbatim, never recompute on export.
- **Disabled logs are included** (run5 ADR-0007). Recount skips logs with
  `disabled_at != null`; a re-import that disables a previously active log must cause
  its derived result to disappear on the next recount.

## What is intentionally NOT exported

- `results` / `member_results` — derived data; chrono-desk recounts from `rfid_logs`.
  (Exception: golden-test fixtures export reference results separately for comparison.)
- Users, payments, organizations — out of scope for the offline helper.

## Reverse direction (v0.3, for reference)

Logs collected offline are uploaded to the site in the existing `FeibotCsvImporter`
format (`EPC:YYYY-MM-DD_HH:MM:SS.mmm,port=X,rssi=Y`), so the site needs no new import
endpoint. Identical ids guarantee dedup.
