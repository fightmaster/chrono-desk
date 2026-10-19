# Photo metadata views without repeated event scans

Task: CHR-DESK-PERF-001
Docs-Impact: LOCAL

Interactive `/photos/merged` and `/photos/status` share an app-owned metadata
projection cache. A hit reads an indexed per-event revision token, not the whole
photo table. Count and merged list use the same cached projection; visible frame
JSON is always fetched from the caller's current read snapshot. Image downloads
remain separate and on demand. Camera ingestion continues with Live closed.

`photo_metadata_revisions` and versioned SQLite triggers change the token in the
same transaction as insert/delete or a meaningful merge-input update (ID, event,
source/camera, crossing time, bib/source or representative URL). Repeated unchanged
polls and fetched_at do not invalidate. Frame growth alone does not require a new
merge; the selected frame JSON is reloaded, so late series completion is visible.
The triggers also cover older application writers after reopening the file.
The schema change is additive and local to photo metadata; raw timing observations,
manual timing decisions, participant state and sync ownership are unchanged.

Every caller supplies one coherent read snapshot covering token, merge inputs and
visible frames. Old readers use their own exact version even after a writer commits;
a new reader cannot reuse a stale projection. Database identity is part of the
version so reopening another event file never reuses an old cache entry. The
context-aware gate coalesces concurrent misses; the writer does not use this gate.
At most four event/window projections are retained. Cache state is disposable;
restart simply rebuilds it, without any timing replay or pending input loss.

Tests cover unchanged/repeated polls, empty events, concurrent misses, correction
and deletion invalidation, transaction rollback, frame completion without a merge
scan, coherent old/new readers, returned-slice isolation, cancellation and entry
retention. HTTP/read-while-write and background-ingest tests remain applicable.

## Owner-Mac evidence, 2026-10-20

An existing sealed native event772963 copy remained on the owner Mac. No working
DB access, HTTP, images, reader or recount started. The copy contains920photo
metadata rows/finishes. The diagnostic executes25 pairs of count+merged-list
loads, with60visible representatives, including JSON encoding/hashing for parity.
Only aggregate timings/counts/hashes leave the machine.

| Same copy, 25 pairs | Uncached | Cached, including first build |
| --- | ---: | ---: |
| Total time | 525.18ms | 186.74ms |
| Mean per pair | 21.01ms | 7.47ms |
| SELECT count | 100 | 76 |
| Rows read | 47525 | 2470 |
| Mutating SQL | 0 | 0 |

Counts, ordered list and visible frame JSON have identical aggregate SHA256;
photo facts are unchanged. The remaining rows include revision-token reads and
60visible frame rows per iteration. One unchanged full metadata scan replaces
50full scans. The old/new phases ran sequentially in one process; these are one
copy/workload measurements, not p99 or a Mac WebView rendering guarantee. The
cache adds memory for up to four metadata projections, proportional to their
merged entries; it does not retain image bytes or all burst frame JSON.

The earlier combined calculation/read-pool evidence remains in the
[performance report](reports/chrono-desk-performance-2026-10-19.md).
