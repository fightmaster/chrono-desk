# Ranking & protocol computation (porting spec from run5)

The checkpoint-derivation engine comes from rfid-sync, but **ranking exists only in run5
(PHP)** — rfid-sync stops at raw `results` + member start/finish/clean times. This doc is
the Go porting spec for run5's "new" results system (member_results + format strategies),
which ran in production at the latest start and is the golden-test reference.

## Two stages

1. **Materialization** — per (member, race) the format strategy computes a result row:
   `{status, rank_primary, rank_secondary, rank_tertiary, payload}`.
2. **Place assignment** — sort materialized rows, assign overall / gender / category
   places.

run5 persists stage 1 into the `member_results` table; chrono-desk may compute both
stages in memory on demand — schema parity is not required, output parity is.

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

v0.1 implements FixedDistance + both category strategies (covers most starts);
TimeLimited and Run5Stopwatch follow the same spec afterwards.
