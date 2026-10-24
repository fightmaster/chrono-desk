# Presenter live feed implementation evidence — 2026-10-24

Task: CHR-DESK-UI-002. Docs-Impact: LOCAL.

Source: branch `feat/chr-desk-ui-002`, based on the combined performance/roster
candidate `5086448` (Desk 0.6.13). Domain: Desk public results presentation and
SQLite read projection. This adds no timing calculation, observation or site-sync
contract version. No actual reader, event file, running Desk or remote
branch was changed.

The presenter receives a bounded committed finish snapshot through a separate
read-only pool, with additive finish-order indexes. The tablet feed, public API,
settings link/QR, privacy rules and test instructions are documented in
[the presenter guide](../presenter-live.md).

Three-client Chromium 1024×768 integration, with Go race detection enabled:

```text
clients: 3
latency samples: 36 (12 new finishes × 3 clients)
p95: 525 ms
max: 540 ms
target: <=1000 ms
concurrent synthetic observation inserts: 1211 over 12.27 s
observed average: 98.7/s; configured target: 100/s
```

Latency starts before sending the fixture finish, includes its SQLite commit,
polling, network response and DOM reconciliation, and ends after two browser
animation frames. It is a synthetic local DOM/paint-opportunity check, not a
physical screen/Wi-Fi or actual reader transport measurement.

The browser passes deduplication, correction, revoked-finish removal, distance
filtering, deliberately obsolete filter response handling, event switching,
roundtrip through the existing protocol tab, scroll anchoring, hidden-tab polling
pause, offline/reconnect, one request in flight, no full-protocol reads during
live mode, and no JavaScript runtime exceptions. The Go race detector reports no
races in this opt-in test. Fixture writes are compiled only into tests.

API/SQLite tests additionally cover latest-50 ordering, opaque process-scoped
identity, PII trimming, published-event scope, rejected unsupported/foreign race
filters, POST rejection, OK/DNS/DNF/DSQ, reserve and TimeLimited exclusions,
corrections/revocation, old-file index migration/idempotence and indexed ordering,
and committed reads while a writer transaction remains open.

Validation commands:

```sh
make quality
CHRONO_PRESENTER_BROWSER=/path/to/chromium \
  go test -race ./internal/transport/publicweb -run TestPresenterBrowser -count=1 -v
go build ./...
govulncheck ./...
```

The full quality gate (frontend clean install/audit/build, unit and runtime
smokes, formatting, vet, staticcheck, all Go tests and all race tests) passes.
Go build passes. The final public-tab adjustment was rechecked with the full
publicweb race suite, including the real three-client browser scenario, and build. Go 1.24.13 and Node 24.18.0 were used; the local runner explicitly
selected those binaries rather than the system's older Node/Snap Go wrappers.
The initial npm audit exposed one high Vite advisory group; compatible lockfile
updates install Vite 8.3.5 and related bundler patches. Clean audit now reports zero
vulnerabilities; no Svelte major upgrade was made.

The separate Go vulnerability audit remains unsuccessful: 25 reachable standard
library advisories on the macOS-11-compatible Go 1.24.13. Six additional imported
package findings and 37 module findings are not called according to the scan.
The newly observed advisory includes GO-2026-6809 (crypto/x509, fixed in Go 1.26.10).
This is tracked under CHR-SEC-002; these checks do not constitute clean security
acceptance or permission to drop Big Sur support.

Raw logs for this implementation are `/tmp/chr-desk-ui-002-quality-final.log`,
`/tmp/chr-desk-ui-002-browser-final.log`, `/tmp/chr-desk-ui-002-vuln.log`;
the tablet screenshot is `/tmp/chr-desk-ui-002-tablet.png`.

Field acceptance remains under the central task: real competition MacBook,
three browser/tablet clients, venue Wi-Fi and actual live observation input.
Source implementation is ready for review. Native candidate delivery is recorded
below; application launch, publication and field acceptance remain separate.


## Native Mac delivery

Owner explicitly requested immediate Mac delivery and enabled SSH key access.
Built on the owner Mac at `192.168.0.106`, macOS 11.7.11, x86_64, using portable
Go 1.24.13 and Wails 2.11.0. Native compilation took 2m48.835s. The build uses
the validated frontend bundle and an offline module proxy; all 11,464 source/tool
file hashes were verified before compilation. A 2.7 MB source delta reused the
previous verified Mac build tools; the interrupted full upload was removed.

This is a private working-tree candidate, not a newly published Git release.
Build identity is `0.6.13-live`, build 224, source snapshot
`working-tree-6ff143fa67fa7a842e23a12114ad1e8de0d6f695eadc13b0d368f4df0e20d8d0`.
The bundle plist remains numeric `0.6.13`. Binary metadata verifies Go 1.24.13,
darwin/amd64, timing-core v0.9.0 and rfid-core v0.4.2, with no module replacements.
Ad-hoc codesign verification passes strict/deep for both built and delivered app.

Delivered directly to the Mac:

- `/Users/fightmaster/Desktop/Chrono Desk Live 6ff143fa67fa.app`
- `/Users/fightmaster/Desktop/Chrono Desk Live 6ff143fa67fa.zip`

ZIP SHA-256:
`9db62c13591c54f8cef503c1c51d96923be2285a81a784776ca5085b37880ee6`.

Build inputs/logs and delivery evidence are retained under
`~/Library/Application Support/chrono-desk/builds/CHR-DESK-UI-002-6ff143fa67fa/`.
Local evidence:
`storage/app/desk-presenter-private/mac-kit-live-6ff143fa67fa/native-verification.json`.
The app has not been launched; the existing app and working event were not
replaced. The operator can close the existing Desk, open the delivered app,
enable broadcast, and test `http://192.168.0.106:8090/live` from the tablet.
Actual GUI/venue Wi-Fi/live-input acceptance remains under CHR-DESK-UI-002.


## Ten-finish revision

The owner requested a smaller feed to reduce Wi-Fi traffic. The SQLite query
now applies LIMIT 10 before serialization, and the page title and regression
expectations match. This reduces the variable row payload to one fifth of the
previous 50-row snapshot; polling remains 500 ms with visibility pause and one
request in flight. Ordering, deduplication, corrections and the team/city fields
are preserved. Validation and subsequent Mac delivery evidence are recorded in
the central CHR-DESK-UI-002 task and private build manifests.


## Club and official places candidate

Task: CHR-DESK-UI-002. Docs-Impact: CROSS_PROJECT.

Club and Team are separate fields. New optional Club display metadata is stored
in a side table, preserves absent versus explicit null on imports/backups, and
is independent of canonical packet Person state. The authenticated operator
refresh fetches configured site metadata once and updates only existing local
registrations. Current site import discarded the Club column; producer fix
cd3d7a3ceae1a796172d59f92c237076aaac128d is prepared separately in RUN5.
Existing event835102 Club data has not been repaired. A minimal source-file
transfer for identity matching was rejected by automatic review pending explicit
owner permission; no transfer or repair is claimed.

Official gender/category places use shared ranking policy and a coherent cache
that ignores raw observation traffic. Latest-ten truncation never changes rank.
The cache covers corrections, status/category policy, clean-time eligibility,
source-store identity and snapshot ordering. Full quality passes frontend/runtime
checks, vet/staticcheck, all Go tests and race tests. Three Chromium clients with
99.6 synthetic observations/s: p95 487ms, maximum488ms over36samples. The populated
tablet fixture shows distinct Club, Team, City and gender/category places;
`/tmp/chr-desk-ui-002-club-places-tablet.png` is synthetic evidence.

Native macOS11 Intel candidate snapshot
`working-tree-a6dff0a54758e625c352b743b3007cdcb433266428f06f37a6976ef075c640ea`
was built with Go1.24.13/Wails2.11.0 after verifying11472source/tool hashes.
Binary module identity and strict/deep signatures pass. Delivered to owner Desktop:
`Chrono Desk Live 10 a6dff0a54758.app` and matching ZIP, SHA256
`f8941253d80f1bfff5d5df10f9b93c698911c9ad1fa095091aea3ac531229bea`.
App not launched; existing event data unchanged. Operator should close old Desk,
open this candidate and reload the tablet live page. Club population requires
producer rollout and separately approved recovery, then «Обновить клубы с сайта».
Private delivery receipt lives in
`storage/app/desk-presenter-private/mac-kit-live-a6dff0a54758/mac-delivery.json`.
