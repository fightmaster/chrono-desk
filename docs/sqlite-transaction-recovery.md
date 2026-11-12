# SQLite transaction recovery

Task: CHR-DESK-TX-001. Docs-Impact: LOCAL.

On the release baseline (Desk 0.6.20, modernc SQLite 1.44.0), a deferred foreign
key violation rejects COMMIT but leaves SQLite inside its transaction.
`database/sql` has already marked `sql.Tx` done: a deferred `Tx.Rollback` cannot
clean it up. Reusing the connection then fails with “cannot start a transaction
within a transaction”. This was reproduced in a test, not reported as a race-day
incident.

All infrastructure transaction owners now use `beginTransaction`. It reserves a
`sql.Conn` before starting `sql.Tx`, keeps it out of the pool until the outcome
is settled, and discards it through `Conn.Raw` / `driver.ErrBadConn` on failure.
Closing the SQLite connection abandons the remaining transaction. Cleanup never
executes `ROLLBACK` on the shared pool: that could roll back another writer.
Cancellation may already discard the connection; repeated cleanup is harmless.
Success returns the healthy connection to the pool without additional SQL or
network work. The original transaction error remains visible to the caller.

The audited owners are:

- `Store.WithinTx` and standalone `ProcessorRepo.WithTx`;
- raw and owned observation insertion, imported raw upserts and event import;
- sync baseline acceptance;
- Club legacy migration;
- packet LAN invitation, claim and revocation;
- read snapshots (rollback-only; the same lease cleanup applies).

Nested writers still join the outer `sql.Tx`. Finish notifications still follow
only a successful outer commit. Processor callback panics now also release the
lease through a deferred rollback. Neither schema nor observation/projection
contracts change; native projection retry remains the separate CHR-DESK-REC-001.

Regression coverage exercises deferred constraint failures in both transaction
entry points while another writer waits, discarded finish/member changes,
absence of premature notifications, context cancellation, callback panics,
injected driver COMMIT failure and atomic raw/outbox ownership. The next writer
and a retry succeed on the same Store. The injected-driver test also counts
connections and prepared statements on success to exclude extra recovery SQL.

Build and release state belongs to the central `chrono-docs/TODO.md`; this
document describes the implementation and does not imply an installed release.
