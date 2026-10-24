# Primary Club implementation evidence

Task: CHR-REG-001. Docs-Impact: CROSS_PROJECT. Source candidate; not installed.

Club is exposed as `domain.Member.Club` while preserving the existing normalized
metadata table for compatibility with the previously delivered UI-002 Mac app.
RUN5 owns canonical members.club/users.club. Team remains independent.

Native registration create/editor/search/results/on-course/passes/live screens,
private member API, trimmed public protocol and public LAN pages now carry Club.
Protocol CSV appends Club after old columns and retains spreadsheet escaping.
The latest-ten presenter feed, deduplication, poll/visibility behavior and cached
gender/category places remain unchanged.

Opening a legacy event captures unclassified old Team once into a provenance
ledger and seeds only unknown Club values. Known explicit null is never revived.
Subsequent Team edits are independent. Modern old-payload import preserves known
Club; only a genuinely new legacy row can seed from Team. Normalization rejects
invalid UTF-8, oversized or control-containing values before partial writes.

Club-only edits use existing audit/replay without recount or ranking revision.
V3 sync negotiates club_affiliation_version2; legacy push refuses to silently
lose Club. For active packet scope, edits capture original owner separately from
canonical Person. Import/refresh compares provided club_person_id under the SQLite
transaction. Stale owner cannot be rebound during push or replay. Derived packet
feed projection transfers historical source Club and clears replacement/release,
without echoing derived affiliation as a local edit back to authoritative site.
Packet Person JSON/hashes/heads and observation identity remain unchanged.

Evidence: all affected service/storage/private API/public web suites pass;
frontend npm audit/build and all runtime, edge and progress browser smokes pass;
Go vet/staticcheck pass after two formatting findings were corrected; go build
passes. Full repository tests and all race tests pass (service race187.954s);
/tmp/chr-reg-001-desk-check.log records the completed gate. Required govulncheck reports25existing Go1.24
stdlib findings (Big Sur compatibility; central CHR-SEC-002), so this is not a
clean security acceptance. No production or active Mac event data changed.

Remaining product scope belongs only to central chrono-docs/TODO.md CHR-REG-001:
independent tablet issuance Club import/editor/channel and backup semantics,
coordinated release proof, production backfill and actual Mac installation/readback.
