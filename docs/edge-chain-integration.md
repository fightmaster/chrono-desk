# Local sidecar / Hub / Desk integration

Task: CHR-SIDE-002
Docs-Impact: CROSS_PROJECT

## Website management integration

`TestEdgeManagementActualHTTPSMySQLAndSidecar` uses the `edgeintegration` and
`edgecentralintegration` tags and the central fixture prerequisites documented
below (`EDGE_RUN5_ROOT`, cached PHP/MySQL/Redis image IDs, actual sync/Hub/sidecar
binaries). Run it with `-count=1 -v -timeout=20m`. It deliberately keeps the real
30-second heartbeat cadence; command delivery and result reporting are separate
exchanges, so this is a several-minute acceptance check, not a fast unit test.

The fixture terminates genuine, certificate-validated TLS on localhost and tunnels
requests to a hidden PHP listener in the network-none container namespace.
The guarded `tests/Support/edge-management-router.php` supplies trusted HTTPS
server metadata as an FPM TLS terminator would. It is copied only into the
synthetic test container; no production middleware or CSRF check is bypassed.
Environment variable `HTTPS=on` alone does not provide this metadata to PHP's
development server. Cookie-jar clients use the real Chrono app host, secure
cookies, login and HTML forms; this is not Chromium or visual phone acceptance.

It exercises administrator registration, ordinary-user denial, missing CSRF,
one-time credentials and both actual source profiles' local enrollment/heartbeat.
Plate receives the four allowed commands through the website and reports durable
results. The TLS test proxy deliberately suppresses the first command reply and
the first committed result acknowledgement with a gateway502; the next real
heartbeats must recover both. The same lost result ID must be acknowledged again,
each command must have one delivery/result audit and exactly one local revision
increment. This is application-response loss, not a physical-network packet-loss
test. The wait budget allows extra30-second exchanges without changing production
cadence or command expiry. Feibot truthfully advertises no mutable-source capabilities. Revocation
must reject the actual client without stopping autonomous local raw capture or
erasing the event/session after restart. Timing rows/source admission must remain
unchanged; no credentials or raw payload bodies are printed in failure output.

This sequential scenario does not prove concurrent revoke/enqueue ordering,
throughput, hardware behavior or release security.
The separate receiver tests below remain responsible for observation delivery.

The Linux-only `edgeintegration` test connects the actual sidecar executable,
actual Hub executable/Redis and production Desk services over TCP. It has no
fake ACK or fake receiver. The Desk Wails window is not involved: its actual
listener, event SQLite, projection and source relay run inside the Go test.

Both source profiles run the same scenario:

1. Start a fresh private sidecar database. Produce a synthetic Feibot CSV file
   or a time-only plate TCP message; require both receiver ACKs and a Desk result.
2. Stop Desk input. Produce another observation and require Hub to progress.
3. Stop/restart the sidecar executable with the same database and restart Desk
   input. Require automatic backlog delivery without new reads, Start or date
   confirmation. Hub's already ACKed entries must not be published again.
4. Pause only the test Hub/Redis container. Produce a third observation and
   require Desk to progress. Unpause and require automatic Hub recovery.
5. Enable the actual Desk source relay. Require scoped ACKs from Hub for all
   three packets; compare persisted event/session/origin/clock/identity and
   physical fields across both paths. Desk retains three facts/results and no
   Desk-owned native-v3 outbox entries.

Plate date confirmation uses the actual authenticated local form with CSRF and
revision. It changes only the application's date evidence, never OS or reader
clocks. After process restart, only **new raw input** requires new date evidence;
the already timestamped backlog is delivered first without it. Feibot input
creates separate synthetic CSV files under one event, never vendor directories.
The fixtures register both the EPC and the bib encoded in its last six hex
digits. Plate preserves that existing bib derivation; a positive packet number
takes precedence over EPC in Desk's member lookup. Raw receipt alone therefore
does not prove a result when the event registration does not match the number.
This suite does not change that source-backed identification policy.

## Running

Build the sidecar and Hub from the intended feature commits. Hub must be a
static Linux executable (`CGO_ENABLED=0`) because the test Redis image uses
Alpine. Supply the absolute executable paths and the immutable image ID of an
already installed `redis:7-alpine` image:

```sh
export EDGE_SIDECAR_BINARY=/absolute/private/artifacts/sidecar
export EDGE_HUB_BINARY=/absolute/private/artifacts/rfid-hub
export EDGE_REDIS_IMAGE=sha256:THE_64_HEX_DIGIT_LOCAL_IMAGE_ID
go test -tags edgeintegration ./internal/service \
  -run '^TestEdgeChainSidecarReceiversAndRelay$' -count=1 -v -timeout=4m
go test -race -tags edgeintegration ./internal/service \
  -run '^TestEdgeChainSidecarReceiversAndRelay$' -count=1 -v -timeout=4m
```

Use the declared repository toolchains and the development core workspace until
the immutable new core version is published/pinned. No dependency downloads are
needed when using the prepared offline cache (`GOPROXY=off`, `GOSUMDB=off`). The
tagged gate fails on missing prerequisites; it does not silently skip or run as
part of an ordinary unit-test command. Record source commits, toolchains,
artifact hashes, image ID and actual test output with the acceptance evidence.

## Isolation and cleanup

Docker is required. The test creates one uniquely named, task-labelled,
non-root, capability-free container running real Hub main and ephemeral Redis,
with `--network none`, CPU/memory/PID limits and no image pull. A transparent
loopback TCP tunnel copies bytes through `docker exec -i ... busybox nc` into
Hub's real TCP socket. No payload or ACK is generated by the tunnel. This avoids
Docker Desktop host-port and filesystem-sharing assumptions. It is test-only
transport plumbing, not another production module or a throughput benchmark.
Hub receives explicit synthetic bindings and an empty working directory;
no repository `.env`, service key or real event is read. The executable is copied
through the Docker API into the disposable container before startup; no host
directory is mounted. Its root filesystem is writable for that copy. Redis runs without
persistent volumes, on container loopback. Test data is disposable and synthetic.
All application destinations are explicitly loopback, PWA/site integrations are
disabled and no external request is part of the test. Do not substitute real
credentials or event configuration.

Sidecar subprocesses receive explicit loopback destinations and no inherited
proxy, credential or system D-Bus address. SIGTERM/restart uses only subprocess
handles owned by this test. Cleanup joins tunnel workers and unpauses/removes
only the created container ID; existing services are not inspected or modified.
Temporary databases/configs/logs are removed by `testing.T.TempDir` cleanup.

Desk's production input binds its selected ephemeral port using its normal LAN
behavior. Run on a trusted development host; this is not a production endpoint.
An external kill of the whole test process can prevent cleanup. In that case,
inspect the `task=CHR-SIDE-002` label and exact recorded test names before
removing leftover resources; do not use global Docker prune commands.

## Evidence limits

The original receiver scenario covers the source-to-receiver and Desk-relay links, not central MySQL
admission/projection, RUN5 feed/export, event-switch/revocation, native vendor
overlap, disk/power faults, sustained load, a physical reader or phone rendering.
Redis in this fixture has no AOF: its successful XADD tests the Hub ACK boundary,
not storage durability after container/host power loss. Ordinary and race test
success do not waive those remaining CHR-SIDE-002 acceptance/release gates.

## Event switch and held historical queues

`TestEdgeChainEventSwitchPreservesHeldBacklog` uses the same `edgeintegration`
tag, binaries and immutable Redis image. No PHP/MySQL prerequisites are needed:

```sh
go test -tags edgeintegration ./internal/service \
  -run '^TestEdgeChainEventSwitchPreservesHeldBacklog$' -count=1 -v -timeout=6m
go test -race -tags edgeintegration ./internal/service \
  -run '^TestEdgeChainEventSwitchPreservesHeldBacklog$' -count=1 -v -timeout=6m
```

Each Feibot/plate subtest creates two separately provisioned, isolated Hub/Redis
receivers for synthetic events 100 and 200. Desk deliberately reuses one TCP
address for the two event databases, stopping one input before starting the
other. This verifies the hazardous case where an address is unchanged but its
event ownership has changed.

1. Capture and ACK one event-100 observation at both receivers. Stop Desk and
   pause only the old Hub fixture; capture another observation and require
   persisted retries to both destinations.
2. Select event 200 and provision the new receivers. Plate uses actual local
   key/CSRF/revision-protected forms, which allocate a new source session.
   Feibot atomically changes only the synthetic effective INI/CSV directory,
   waits for the source-selection poll, then emits fresh CSV to activate it.
   This does not claim to drive a real vendor UI or exercise its INI importer.
3. Require a new event-200 result at both receivers while the two event-100
   deliveries remain visibly held. Restart sidecar on the same database with
   no new input: the selected event, held packets and existing ACKs must survive.
4. Explicitly select the old session while the destinations still serve event
   200. Plate stays input-paused; Feibot uses its existing authenticated
   maintenance Start. Require actual Hub binding rejection and Desk wrong-event
   rejection, persisted failure attempts and no new event-200 facts/publications.
5. Correct the receivers/endpoint for event 100. Require automatic backlog
   recovery without a new read or clock confirmation. Compare unchanged source
   wire, event, raw text and acceptance time before/after the whole sequence;
   compare Desk's original envelope bytes and Hub event/session/origin/clock
   fields. Previously ACKed delivery timestamps and attempt counters must not
   change. End with two event-100 facts/results and one event-200 fact/result,
   no native outbox entries, and every source delivery ACKed.

Only the final historical recovery permits up to two minutes: the real
Feibot sender retains exponential row backoff (under 60 seconds with jitter)
and may add a 30-second circuit interval before a due probe. A sidecar runtime
regression records a real fifth-failure delay of 32.730208257 seconds, preserves
it across same-configuration runtime restart and endpoint correction, and proves
unchanged source/earlier ACK state when the row becomes due. The former blanket
30-second wait was shorter than valid retry policy. Normal setup/live checks
keep 30 seconds; the source-load rate/backlog/drain thresholds are unchanged.
This is a bounded recovery test budget, not a new field-latency SLO or a change
to production retry rules. Old timeout logs lacked the deadline snapshot, so
the exact cause of that historical run cannot be reconstructed from this proof.

The test reads sidecar SQLite using `mode=ro` and `query_only` solely for
assertions. It never inserts or updates source rows directly. All settings
changes go through the application's normal local commands or the synthetic
Feibot configuration reload boundary. Production services/binaries are unchanged.

The source helper waits for the actual plate TCP socket before sending its first
byte, using the existing bounded receiver-readiness helper. The local form saves
desired intake state; it does not promise that asynchronous socket binding has
already completed. A refused connection may retry during this readiness window;
partial/failed writes are never hidden by replay. The source-load timer still
starts only after preparation, so its offered rate and lag limits are unchanged.

Operationally, "automatic resend" applies to the selected session. Switching
events holds old queues; it does not discard them or reinterpret their event.
Restore receivers for the original event, select that historical session and
let its saved queue drain. Returning to an existing plate session uses its
session selector, not a new identity generated by editing event settings again.
The test's deliberately wrong-target step is a rejection test, not recommended
operator procedure. Local input pause is not physical reader Stop.

This closes the local source/Hub/Desk event-switch scenario, not central MySQL
event-switch acceptance, Desk HTTP administration, reader/phone hardware,
power-loss durability, mixed native historical aliases or load testing.

## Optional source load and resource measurement

The additional `edgeload` tag enables `TestEdgeSourceSustainedLoadAndBacklog`.
It reuses the actual sidecar executable/configuration/startup helpers but replaces
both receivers with **synthetic in-memory peers using the shared core listener**.
No Docker, Hub, central database or Desk projection is involved in this test:

```sh
export EDGE_SIDECAR_BINARY=/absolute/private/artifacts/sidecar
go test -tags 'edgeintegration edgeload' ./internal/service \
  -run '^TestEdgeSourceSustainedLoadAndBacklog$' -count=1 -v -timeout=7m
```

Each Feibot/plate profile receives 15,000 unique synthetic observations over
60 seconds at a scheduled 250/s (25 every 100 ms). Feibot appends and rotates
5,000-row CSV files, using the shipped batch-size default of 500. Plate sends
time-only JSON on one real TCP connection after the existing local date form.
One peer refuses publication during the second half; the other must keep up.
After all facts have been committed and the healthy peer ACKed, the executable
is stopped/restarted on the same database and the second peer recovers without
new reads, Start or clock confirmation. Every peer packet digest must equal the
source's stored envelope, original source rows must remain unchanged, and the
healthy peer must not receive its already ACKed history again.

The gate fails if producing the stream takes more than 65 seconds, falls over
two seconds behind schedule, or the healthy peer falls over ten seconds behind
the offered stream. These are coarse **local smoke limits**, not accepted
appliance latency/resource budgets. Read-only `/proc` sampling records only the
sidecar process's peak RSS, threads and file descriptors; file sizes track its
database/WAL/SHM and process logs. OS process accounting supplies CPU time across
both executions. Test-runner/receiver resources are excluded. The gate reports
these measurements without pretending they prove a long-term retention bound.

This test adds no production instrumentation or runtime overhead. It is a short
host-side throughput/recovery measurement, not a Raspberry Pi benchmark, vendor
CSV flush/UART assessment, disk-full/power-cut test or actual receiver load test.
Use the original receiver tests separately for actual Hub/Desk correctness.
The race detector covers the test process only unless the supplied sidecar was
built with it; race-instrumented results are not comparable throughput evidence.

The first measured source checkpoint uses sidecar `3c0696e`. Feibot passes this
short gate after independent worker scheduling and single-connection SQLite
queueing; plate still fails the live rate/capture deadline. Do not weaken the
threshold or label that combined test green: the plate callback at that checkpoint commits
each frame separately, and at the input deadline only 7,338 of 15,000 offered
frames were committed. The failing fixture is stopped after its deadline, so this
is not evidence of a production loss incident or a completed plate replay.
Keep the failing load gate separate from passing ordinary unit/race suites.

The later core `cc8efd6`/sidecar `b032615` checkpoint passes this same unchanged
gate for both profiles (167.644 s combined). Bounded raw capture and ACK commits
retain WAL/FULL durability and per-frame clock evidence. Maximum healthy
backlog/rate is 1.1 s Feibot and 0.4 s plate, with all 15,000 facts preserved and
both queues recovered without new input. Source RSS is 25.35/24.20 MiB. See
chrono-docs `reports/edge-source-batching-and-startup-2026-09-10.md` for exact
artifacts, intermediate failures and limits. This is not actual receiver-chain
revalidation on the new binaries, maximum throughput or Raspberry Pi acceptance.

## Optional real plate browser workflow

`TestPlateLocalBrowserWorkflow` uses `edgeintegration edgebrowser`, the same
actual sidecar subprocess fixture and an installed Node 22+ / Chromium binary.
It needs no Docker and installs no browser or npm package:

```sh
export EDGE_NODE_BINARY=/absolute/node22/bin/node
export EDGE_BROWSER_BINARY=/absolute/chromium/chrome
export EDGE_BROWSER_ARTIFACTS=/existing/private/output-directory
go test -tags 'edgeintegration edgebrowser' ./internal/service \
  -run '^TestPlateLocalBrowserWorkflow$' -count=1 -v -timeout=3m
```

The script controls Chromium through its local debugging socket; the actual
embedded HTML/JS and sidecar HTTP handlers are used, without a frontend API mock
or injected replacement page. It verifies no-access refusal and the shared
Basic key path, then uses a synthetic header key for page actions. It exercises
360/390/768-pixel layouts, the visible startup guide, settings save, stale revision
and missing-CSRF refusal, the phone-time touch button, date confirmation and
intake enable/disable. After a process restart it checks saved settings, reviews
an intentionally 12-hour-old time-only capture using preview then explicit
single-record confirmation, and compares unchanged raw bytes, clock samples and
previously accepted observations. A second restart must preserve input pause,
both independent retry queues and exported facts. No new read is needed to
inspect or recover state.

Both destination addresses are reserved rejecting local listeners: this tests
visible retry persistence, not recipient ACKs or hardware. The original receiver
chain remains its separate gate. No host/reader clock or NTP command is submitted.
Browser request interception allows only this fixture's exact loopback origin
and allowlists the harmless application actions; hardware and external requests
fail the test. DNS/proxy flags also restrict browser background networking.
The runtime may inspect its normal local OS clock service, but the test never
changes it or assumes it supplied date evidence.

Screenshots and saved **synthetic** settings are retained in a newly created
directory only when `EDGE_BROWSER_ARTIFACTS` is explicitly set; otherwise testing
cleanup removes them. Browser profiles, subprocesses, temporary databases and
reserved listeners are always closed. This is browser emulation, not an iPhone,
Android handset, native WebKit, physical reboot or installed-device acceptance.
Use both full Chromium and headless shell explicitly when recording evidence.
The Desk frontend smoke is independent and uses a synthetic API, not the plate
process. It now waits for explicit UI completion over inherited Chromium debug
pipes and closes its own browser, instead of relying on `--dump-dom` process
termination. Both full Chrome and headless shell must still pass that separate
gate; a passing plate workflow alone does not prove the Desk UI.

## Optional central MySQL / RUN5 gate

The additional `edgecentralintegration` tag enables
`TestEdgeChainCentralAdmission`. It reuses the complete receiver scenario above,
then starts the actual rfid-sync executable against its saved Redis backlog.
Two additional disposable containers share Hub's network-none namespace: MySQL
and a PHP CLI runtime with `pdo_mysql`. No published port or Internet is needed.

Supply the existing variables above plus:

```sh
export EDGE_SYNC_BINARY=/absolute/private/artifacts/rfid-sync
export EDGE_MYSQL_IMAGE=sha256:THE_64_HEX_DIGIT_LOCAL_MYSQL_IMAGE_ID
export EDGE_PHP_IMAGE=sha256:THE_64_HEX_DIGIT_LOCAL_PHP_IMAGE_ID
export EDGE_RUN5_ROOT=/absolute/run5-feature-checkout
go test -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentralAdmission$' -count=1 -v -timeout=6m
go test -race -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentralAdmission$' -count=1 -v -timeout=6m
```

The RUN5 checkout must contain `tests/Support/edge-chain.php` and its installed
vendor tree. The test copies an explicit list of tracked source directories from
`HEAD`, vendor and that exact test helper through the Docker API. It does not
copy `.env*`, ignored bootstrap caches, uploads or logs. Uncommitted production
source is intentionally not tested: commit it before claiming this gate covers
it. The helper refuses non-CLI/non-testing execution, non-loopback MySQL, a
different database/user, environment files or cached application configuration.
Never invoke it against a real database or substitute production credentials.

The helper runs all actual Laravel migrations, seeds six synthetic entrants
without model observers, and grants the exact source through RUN5's public
application contract. The Go consumer must write three distinct raw facts,
results and member results despite receiving both direct and Desk-relayed copies.
MySQL trigger-generated change-feed rows must also remain unique. Assertions
compare physical/source/event/clock metadata with Desk's captured original wire,
the real PHP export and feed serializers. The actual Desk export importer then
consumes the PHP document without creating either outbound journal.

After PHP revokes the source through the same audited contract, the fourth
observation may be ACKed by Hub and Desk, but must remain pending in Redis across
repeated consumer deliveries without changing central facts/results. Explicit
re-enable must drain it automatically; the audit must be grant/revoke/enable.

MySQL uses a disposable 512 MiB tmpfs, 768 MiB memory cap and one CPU; PHP has
the same memory/CPU cap. Trigger creation is enabled for the synthetic schema
user via MySQL's `log_bin_trust_function_creators` fixture setting. These are
test-server settings, not production configuration recommendations. Cleanup
removes only test-created container IDs and their anonymous volumes.

This extends evidence to actual central schema/admission/projection, serialized
PHP feed/export and Desk export import. It is **not** an HTTP transport or admin
permission/CSRF test, event-switch acceptance,
or a deployment/handset/performance gate. The external Go executables are not
race-instrumented merely because the Desk test uses `-race`.

### Cross-language source-admission fence

The same prerequisites also enable `TestEdgeChainCentralPHPGoRevocationFence`:

```sh
go test -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentralPHPGoRevocationFence$' -count=1 -v -timeout=4m
go test -race -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentralPHPGoRevocationFence$' -count=1 -v -timeout=4m
```

Run the full default race suite separately from the long central scenarios:

```sh
go test -race ./... -count=1
go test -race -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentral' -count=1 -v
```

Combining all optional central/receiver scenarios and the default golden
imports in one package run can exhaust Go's ten-minute package timeout. These
separate commands retain every selected assertion and all per-scenario
deadlines; a timed-out combined run is not a passing gate. Source-load and
browser checks remain separately opt-in as described above.

It starts the actual plate source, Hub/Redis, sync and RUN5/MySQL fixture, then
checks three interleavings with real application transactions:

1. Go holds its shared source binding lock and waits inside the fourth raw
   INSERT. PHP revoke must wait for that Go connection. The accepted fact
   commits before revoke, and both raw data and audit remain correct.
2. PHP holds its exclusive binding lock while revoke audit is pending. Go
   must wait for that PHP connection, then reject after its commit. Hub/Desk
   ACKs do not authorize central acceptance; Redis retains and retries the
   packet. Explicit re-enable drains it without source resend.
3. PHP holds the same lock, but the audit INSERT is deliberately rejected.
   Permission and audit roll back together. The waiting Go process admits
   its packet under the unchanged grant, preserving original metadata.

Ordering is proved by `performance_schema.data_lock_waits`, mapped to actual
MySQL connection IDs and the exact synthetic table, not inferred from elapsed
time. Test-only row barriers/triggers are installed by a guarded fixture action;
no production query, lock policy, observability loop or application hook is
changed. The observer uses only the synthetic MySQL container's root identity.
The barrier and PHP commands have 45-second contexts and close/cancel/join on
cleanup. Normal source/receiver/retry deadlines are unchanged.

PHP's standalone driver explicitly exits nonzero on uncaught action errors;
the failure scenario checks both the injected error and exit status. Final
assertions compare six raw/projection/feed/export records to Desk's original
source journal. Native v3 still has no ownership of imported edge data. This
gate covers source grant/revoke transactions, not HTTP/admin CSRF, general
member progression or the separately tracked event epoch mechanism.

### Real HTTP administration and downloads

`TestEdgeChainCentralHTTPSourceAdministration` additionally requires the RUN5
checkout's built `public/build/manifest.json` and assets:

```sh
go test -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentralHTTPSourceAdministration$' -count=1 -v -timeout=5m
go test -race -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentralHTTPSourceAdministration$' -count=1 -v -timeout=5m
```

The guarded fixture seeds two synthetic existing-site users and a second event.
It does not install an authentication bypass or test route. A PHP built-in HTTP
server runs the tracked `public/index.php` and normal middleware, with file
sessions inside the disposable container. `cli-server` requests exercise CSRF;
they are not Laravel's usual in-process console/testing requests. The real login
form and cookies establish each browser session. Separate explicit site
activation/login is required for the cross-site test user.

The existing network-none Hub namespace also contains the HTTP server. Each
finite request is serialized through a bounded `docker exec -i ... nc` exchange
and parsed as an actual HTTP response. Laravel generates every status, header,
cookie and body; the bridge supplies none of them. Finite stdin is important:
the persistent RFID tunnel cannot delimit a close-framed HTTP response. No host
port, DNS/public connection, production credential or extra application service
is needed. Production HTTP/reader behavior and timeouts are unchanged.

The scenario verifies guest and missing-permission refusal, missing/wrong/stale
CSRF rejection, exact board matching, cross-event/cross-site/public-host refusal,
escaped and authenticated audit, idempotent form retry, and HTTP revoke/resume
controlling actual Go admission and retained Redis work. It generates the real
event API token through the existing admin form, checks missing/wrong/other-event
token refusal, then downloads admin export and API feed/export. Their original
source metadata is compared with the Desk journal and imported without an
outbound echo. This is HTTP/schema acceptance, not a browser rendering, TLS,
physical-device, two-event backlog-switch or standalone release gate.

### Central two-event backlog recovery

`TestEdgeChainCentralEventSwitchPreservesHeldBacklog` reuses the existing
Feibot/plate receiver-switch scenario, with optional central observers:

```sh
go test -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentralEventSwitchPreservesHeldBacklog$' -count=1 -v
go test -race -tags 'edgeintegration edgecentralintegration' ./internal/service \
  -run '^TestEdgeChainCentralEventSwitchPreservesHeldBacklog$' -count=1 -v
```

Unlike the single-receiver fixtures, this scenario creates one labelled Docker
`--internal` bridge and two Hub/Redis namespaces. Each actual consumer uses its
receiver's Redis and the same disposable MySQL schema. The new receiver resolves
MySQL only from the old fixture container's inspected private IP. PHP still
shares that old namespace and its guarded `127.0.0.1` connection. No host port
is published, image pulled, production credential used or public endpoint called.
Cleanup removes the owned containers and then the internal network. Two actual
Hub processes cannot share one namespace: their fixed HTTP/pprof ports collide.
The test does not modify production listeners to accommodate its topology.

The guarded PHP `switch-setup` creates event 200 with the same board, EPCs and
bib numbers as event 100, an independently authorized session, and a mutable
native board mapping pointing to 200. Event-scoped snapshots include actual
raw rows, passes, member outcomes, finished members, feed and export. The test
checks old-state stability while new capture progresses, retained old work
across source restart, actual wrong-event receiver rejection, and unchanged
source history after explicit recovery. Old facts must remain in event 100
despite the native board mapping; event 200's facts and results must stay
unchanged. Both exports import into Desk with preserved metadata and neither
outbox populated. Original receiver assertions/deadlines remain active.

This is synthetic software acceptance with real application processes and SQL,
not a maximum-load, physical plate or published-release gate.
