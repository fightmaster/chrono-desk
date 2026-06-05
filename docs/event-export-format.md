# Event export format (run5 → chrono-desk)

Contract for the run5 artisan command `event:export {event_id}` (to be implemented on the
site) and the chrono-desk importer. One JSON document per event.

Re-import is an upsert by `id` (all ids are strings); importing the same export twice is
a no-op, importing a newer export overwrites site-owned data and triggers a recount.

```json
{
  "schema_version": 1,
  "exported_at": "2026-06-05T12:00:00+03:00",
  "timezone": "Europe/Moscow",
  "event": {},
  "laps": [],
  "races": [],
  "categories": [],
  "checkpoints": [],
  "members": [],
  "rfid_logs": []
}
```

`timezone` — IANA zone of the event; used as the default when importing Feibot
flash-drive CSV (which carries zoneless local time).

## Entities

Field sources: run5 `app/Models/*` and `database/migrations/*`. Datetimes are ISO 8601
strings with offset unless noted; `rfid_logs.time` is unix milliseconds.

### event

`id`, `name`, `slug`, `date`

### laps

`id`, `name`, `slug`, `description`

### races

`id`, `event_id`, `name`, `date`, `started_at`, `lap_id`, `format`
(`FixedDistance` | `TimeLimited` | `Run5Stopwatch`), `time_limit_seconds`,
`category_excludes_top_by_gender`

### categories

`id`, `name`, `min`, `max`, `gender`

Protocol grouping derives the category list per race from members (`member.category_id`),
mirroring run5 `Race::categoriesUsedByMembers()`.

### checkpoints

`id`, `event_id`, `race_id`, `name`, `type` (1=START, 2=CHECKPOINT, 3=FINISH), `sort`,
`board`, `since`, `since_offset_seconds`, `sleep_after_prev_seconds`

Required for any recount — a log is matched to a checkpoint by `board` and the
eligibility guards. **An export without checkpoints cannot be recounted.**

### members

Core: `id`, `event_id`, `race_id`, `category_id`, `number` (bib), `epc`, `rfid`,
`first_name`, `last_name`, `gender`, `dob`, `city`, `team`, `status`
(MemberStatus enum: ok/DNS/DNF/DSQ)

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
