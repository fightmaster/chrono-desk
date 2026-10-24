# Presenter SSE synthetic acceptance — 2026-10-25

Task: CHR-SSE-001. Docs-Impact: LOCAL.
Scope: source validation for release0.6.19; platform publication/delivery is
recorded separately in chrono-docs. Baseline: mainb027a1e.

## Runner and source boundary

Linux/amd64, Go1.24.13, Node22.22.0 and local headless Chromium. All events,
participants, readers and network peers were synthetic/temporary. No production
device, working Mac event or Telegram recipient was used by these tests.
The shared cores, dependencies, schema and timing/ownership contracts are unchanged.

The successful30m binary SHA256 is
`194b9f62e4016775fe26d92b08ce5c08a4105ced017ee781f9949f9e8d2c5a4e`.
Its133 backend source/module checksums were checked again against the release
source after completion: no differences. VERSION/Wails release metadata and
additional focused lifecycle tests do not change that backend code.

## Gates

`make quality` passed: private-module preflight, npm install/audit/build,
frontend unit/runtime/edge/progression/window/dual smokes, gofmt, vet,
staticcheck, all Go tests and all Go race tests. Chromium presenter tests were
enabled in both ordinary and race runs. `go build ./...` passed separately.
Final focused race tests additionally verify active-stream cancellation on store
close and40000 concurrent notifications with a full wake queue and concurrent
signal close. Both pass.

`govulncheck ./...` exits3 with the existing Go1.24 baseline:25 reachable
standard-library findings,6 imported-package findings and37 required-module
findings not called by this code. No new dependency was added. Existing
CHR-SEC-002 is not closed or represented as a clean audit.

## Latency and ingestion

Three independent1024×768 browser clients received100 new finishes each, with
approximately100 concurrent synthetic raw inserts/second. There were300 samples:
commit-to-DOM plus two animation frames p95=51ms, maximum67ms; synthetic trigger
to DOM p95=56ms. The lower250ms target and existing1000ms regression guard pass.
This measures runner paint opportunity, not venue Wi-Fi or a physical display.

Three matched repetitions per mode used actual combined native/sidecar TCP
decoding, persistence, projection and ACK,1000 registrations,200 measured ACKs
per repetition and100Hz targeted input. SSE-on has two draining clients and one
nonreading client.

| Mode | ACK p95 repeat1 | Repeat2 | Repeat3 | Median |
| --- | ---: | ---: | ---: | ---: |
| SSE off | 9.487ms | 8.479ms | 8.043ms | 8.479ms |
| SSE on | 8.756ms | 8.513ms | 8.412ms | 8.513ms |

The difference is about0.034ms/0.4%, within baseline noise1.444ms. The frozen
rule is baseline median+10%+observed baseline range (minimum250µs scheduler floor),
giving a10.771ms budget; it passes. This compares ingestion ACK latency, not
presenter request counts or battery life.

## Resource regression

1000 real HTTP connect/read/cancel cycles plus warm-up return owned subscribers,
view groups and workers to zero and reader leases to zero. Sampled goroutine
count is8 throughout; FD/goroutine growth checks pass. Recorded post-GC/profile
heap deltas at250/500/750/1000 cycles were12448/86240/100280/156656 bytes, below
the predeclared2MiB allowance. The sampled retained-heap profile difference has
no application allocation entry.1000 independent hub reconnects also pass with
zero owned resources, as do100 cancellations during an initial blocked build.

Two actual HTTP clients receive successive commits and fresh reconnects; a burst
of30 leaves ten unique latest rows. A blocked writer and a real TCP peer with
constrained buffers do not stop other clients or producer commits. The peer is
removed by its write deadline. Actual native/sidecar ACK completes while an SSE
writer remains blocked.100 unmatched reads and duplicate frames do not advance
the finish generation; ownership/raw counts remain exact. Manual edit/recount
uses one outer commit notification. Rollback, commit failure and forced projection
failure do not announce a finish; sidecar admission rolls back with projection.

## Thirty-minute run

The independently retained test binary completed a30m0.019s run (1801.66s
including setup) with4096 fixed registrations, two draining clients and a
nonreading client reconnected every2s. Input target was100 observations/second,
with finishes and bursts. The initial30s raw-only interval did not build a
snapshot. Every attempted/ACKed input was checked against persisted raw counts.

| Measurement | Result |
| --- | ---: |
| Persisted observations / matching attempted inputs | 172953 |
| Confirmed synthetic finishes | 2540 |
| Actual generated input rate | 96.08/s |
| Snapshots received by each normal client | 1823 / 1823 |
| Shared builds, including reconnect initial reads | 2710 |
| Recorded heap delta from warmed state | +368120 bytes |
| Active-run goroutines / view workers | 31 / 1 |
| FD samples | 31 throughout |
| External RSS range | 29792–36912KiB |
| External OS threads | 10 throughout |

The rate is the observed target-driven generator rate under concurrent runner
work, not a claim of exactly100 accepted frames every second. No attempted input
or ACK was lost. Both clients received the final committed finish. Closing all
clients returned view/worker/subscriber counts and reader leases to zero;
remaining fixture-owned/background goroutines were16 before test cleanup.

Go heap/goroutine profiles were recorded each minute, supplemented by103 external
RSS/FD/thread samples every15s. Profiling is test-only. Heap measurements include
profile/runtime cache noise and normal bounded rank-cache population; RSS alone
was not used to infer leaks. The frozen soak allowance is32MiB beyond warm-up;
the stricter client-only2MiB regression is separate. These results provide leak
regression evidence, not a proof against every possible lifetime bug.

## UI and compatibility

Browser coverage includes idle/no JSON polling or card mutations, one source per
active view, forced Club refresh, distance changes, stale fetch/source callbacks,
deduplication, scrolling, correction/revocation, offline/reconnect, event switch,
Protocol round-trip, hidden/inactive cleanup, old-server polling fallback,
malformed frames and the real15s silent-stream watchdog. The controlled lifecycle
check repeatedly starts/stops views and verifies closed sources remain ineffective.

## Remaining external boundaries

Mac/Windows packaged binaries, GitHub release and owner-Desktop delivery require
their own source/version/signature/checksum receipts. Field Wi-Fi and battery
measurements are not inferred from this runner. CHR-DESK-TX-001 owns the separately
observed deferred-FK failed-COMMIT connection recovery issue; CHR-DESK-REC-001 owns
native raw/projection crash recovery. Neither is claimed fixed by SSE.
