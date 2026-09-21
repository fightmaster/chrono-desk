# Append-only roster compatibility

Task: CHR-SW-019
Docs-Impact: CROSS_PROJECT

The existing site feed accepts a `server_change` action containing a registration
creation (`before: null`). The regression test
`TestSiteRosterAppendPreservesLocalParticipantAndRelaysCreation` parses the wire
page, applies it through the real SQLite transaction, checks preservation of a
local issued participant and verifies that the creation reaches the LAN feed.
No production code, schema or dependency change is needed for this case.

The focused test and full `go test ./...` passed with Go 1.24.13 and a temporary external workspace
replacement of rfid-core v0.4.2 by the clean exact tagged local tree `8254f04d`.
The repository go.mod/go.sum remain unchanged. This is SQLite service acceptance,
not an installed macOS release or a physical tablet LAN test.
