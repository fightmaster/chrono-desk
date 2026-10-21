# Connection history

Task: CHR-OBS-001. Docs-Impact: CROSS_PROJECT.

Desk enables the shared core's local bounded connection history in
`<CHRONO_DATA_DIR>/diagnostics` (or the normal application data directory).
It is separate from event SQLite and raw/outbound observations. A storage
failure disables diagnostics or increments the visible storage-error counter;
reception, processing and ACK do not wait for the diagnostic disk writer.

Live's existing status response includes journal health. No additional polling
request is made. Existing heartbeat age now also gives an explicit stale warning
after 30 seconds. This is evidence of no recent observed heartbeat, not proof of
reader power loss. The receiver continues on every application tab as before.

The Live "Журнал соединений" button exports a JSONL bundle to Downloads through
an authenticated localhost-only POST. It does not send anything to the website.
The journal covers every event/listener in this Desk process and retains earlier
process boots within two rotating 1 MiB files. Queue capacity is 256, ceiling 64
records/second; export reports suppressed records and storage failures for the
current process. Files contain connection metadata only: version, random boot
ID, configured listener, connection number, time, state/error class and last
complete frame age, plus optional validated Feibot reader code (for example
U659/U660) from existing native heartbeat. No chip, participant, address, payload
or error text. Before a valid heartbeat the reader is unknown; a reconnect gets
a new connection number and is attributed after its next heartbeat. Invalid or
oversized codes are omitted; this receiver-local observation is not authenticated
identity or event binding. Generic/Edge sockets lacking native heartbeat remain
unknown. Unchanged heartbeats never add timeline rows.

An active export includes already-written records; it does not wait for all
queued transitions. Stop reception before export if the final disconnect must
be present. History supports network incident reconstruction; it cannot by
itself measure SQL/algorithm/UI time or identify the physical cause of AP loss.
Release 0.6.17 pins published rfid-core 0.5.0 and timing-core 0.12.0.
Release verification uses those immutable module versions without local replaces.
