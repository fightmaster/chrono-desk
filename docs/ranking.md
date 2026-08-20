# Ranking & protocol computation

RUN5 PHP remains the production migration reference, while Chrono Desk and rfid-sync
now consume the same immutable `timing-core` release. The shared module owns pure format
outcomes, rank ordering and overall/gender/category place assignment. This document
defines the application adapter and protocol policy around that shared implementation.

## Two stages

1. **Materialization** — per (member, race) `timing-core` computes a result row:
   `{status, rank_primary, rank_secondary, rank_tertiary, payload}`.
2. **Place assignment** — `timing-core` sorts materialized rows and assigns overall /
   gender / category places.

run5 persists stage 1 into the `member_results` table; chrono-desk converts its SQLite
rows to shared inputs and computes both stages in memory on demand — schema parity is
not required, output parity is.

## Common rank-row ordering (all formats)

- `status='ok'` rows rank ahead of `dns`/`dnf`/`dq` rows.
- Order by `rank_primary DESC`, then `rank_secondary DESC`, then `rank_tertiary DESC`;
  nulls sort last within each level. **Sort must be stable** — run5 has no explicit
  tie-break, input order is preserved.
- Dense places `1..N` assigned to 'ok' rows only; non-ok get `place = null`.
- Status mapping: MemberStatus enum 1=`dns`, 2=`dnf`, 3=`dq`.

## FixedDistance

- Eligible: member has a status code, OR all of `{start_time, finish_time, clean_time}`.
- Status code present → row with `status` string, all ranks null, payload null.
- Times present → `cleanTimeMs = finish_time - start_time`;
  `status='ok'`, `rank_primary = -cleanTimeMs` (negated: smaller time wins under DESC),
  `rank_secondary = rank_tertiary = null`, `payload = {cleanTimeMs}`.
- Neither → no row (member absent from protocol).

## TimeLimited

- Window: `[start_time, start_time + race.time_limit_seconds]` (ms).
- Last `Result` in window (by `time_ms DESC, id DESC`) determines:
  `elapsedMs = max(0, passMs - windowStartMs)`,
  `rank_primary = checkpoint.sort` (further checkpoint wins),
  `rank_secondary = -elapsedMs` (earlier wins), `rank_tertiary = null`,
  `payload = {lastCheckpointId, lastCheckpointName, lastPassAtMs, elapsedMs}`.
- Compat side effect in run5: member.finish_time = passMs, clean_time = formatted elapsed.
- Result label shows `checkpointName, wallClockTime, elapsedMs` (not cleanTimeMs).

## Run5Stopwatch

- Slot = 1-based chronological position of the member's finish crossing among ALL finish
  crossings (claimed or unclaimed), ordered `time_ms ASC, id ASC`.
- `status='ok'`, `rank_primary = -slot`, `payload = {slotIndex, cleanTimeMs}`.
- Place = slot, **gaps preserved** (unclaimed crossings keep their slot numbers).

## Place assignment

- **Overall**: iterate protocol order; explicit place from the materialized row when set,
  else sequential `1, 2, 3...`.
- **Gender place**: independent counter per gender.
- **Category places** — strategy switched by `race.category_excludes_top_by_gender`:
  - *Standard*: dense counter per category key in finish order; members without category
    get no category place.
  - *ExcludeTopByGender*: `TOP_PER_GENDER = 3` (hard-coded). First pass marks top-3
    finishers per gender as excluded; second pass assigns category places skipping them —
    overall medalists don't also take category medals.
- DNS/DNF/DSQ rows render in the protocol but receive no places.

## Top-3 / awards

Leaderboard = category places map filtered to `place ≤ 3` per category (plus overall
top-3 per gender). Source: `RaceResultsPageAction::leaderboard()`.

## Source files in run5 (`~/projects/run5`)

| Piece | Path |
|---|---|
| Strategy contract | `app/Results/Format/Contracts/RaceFormatInterface.php` |
| Factory | `app/Results/Format/RaceFormatFactory.php` |
| Strategies | `app/Results/Format/Strategies/{FixedDistanceFormat,TimeLimitedFormat,Run5StopwatchFormat}.php` |
| Payload DTOs | `app/Results/Format/Payloads/*.php` |
| Protocol query + rankRows | `app/Results/Format/RaceProtocolQuery.php` |
| Rebuilder | `app/Results/Services/RaceResultModelRebuilder.php` |
| Place calculation | `app/Results/Services/{RaceRankingCalculator,RaceResultsPageAction}.php` |
| Category strategies | `app/Results/Services/Ranking/{Standard,ExcludeTopByGender}CategoryRankingStrategy.php` |
| Recount orchestrator | `app/Services/RecountRfid.php` (wipe derived → replay logs by time) |

## Porting order

v0.1 implements FixedDistance and TimeLimited + both category strategies;
Run5Stopwatch follows the same spec later.

## Parity with rfid-sync (verified 2026-06-05)

rfid-sync d54c88b now materializes member_results on the live ingest path — the
reference **Go** implementation of the format math. chrono-desk parity was verified
against it line by line:

- FixedDistance: `rank_primary = -cleanTimeMs`, negative clean → no row — identical.
- TimeLimited: last pass in `[start, start + limit]` by `time_ms DESC, id DESC`,
  `rank_primary = checkpoint.sort` (not negated), `rank_secondary = -elapsedMs`
  clamped at 0 — ported into `internal/ranking` + `Store.LastPassesInWindow`.
- Intentional difference: rfid-sync skips judge-status members (PHP owns dns/dnf/dq
  rows) and writes only "ok"; chrono-desk's ranking renders status rows itself because
  here it IS the protocol reader — that mirrors run5's PHP, the canonical source.
- Intentional difference: rfid-sync requires only valid start/finish times;
  chrono-desk (like PHP) also requires `clean_time` presence — equivalent in practice
  since our recount always sets clean with finish.
- `FormatCleanTime` zero-padded millis (their 2e9087e fix) — locked by a regression
  test in `internal/processor/format_test.go`.

## Known gap in run5's ONLINE flow (discovered 2026-06-05)

During a live race, rfid-sync writes `results` + member times directly to MySQL and does
NOT materialize `member_results`; Laravel has no scheduler/observer that does it either
(`materialize()` runs only from PHP write paths: PushResult/recount, manual entry, judge
corrections, race edit, backfill command). Live surfaces survive via the empty-fallback
to legacy `protocolMembers()`, but freeze on a stale snapshot once `member_results` is
non-empty mid-race, and TimeLimited races aren't covered by the fallback at all (their
member times are set by `materialize()` itself). Planned run5 fix: watermark-based
scheduled materializer over new `results` rows.

chrono-desk is immune by design — ranking is computed in-memory on demand, there is no
materialized table to go stale.
