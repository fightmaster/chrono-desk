package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

type packetRoundTrip func(*http.Request) (*http.Response, error)

func (fn packetRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestPacketRelayEnrollmentAndBootstrapUseSeparateCredentialsAndStrictContracts(t *testing.T) {
	previous := syncHTTPClient
	t.Cleanup(func() { syncHTTPClient = previous })
	requests := 0
	expires := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			if request.URL.String() != "https://app.chrono.events/api/sync/events/621632/packet-issuance-relays" ||
				request.Header.Get("X-SYNC-TOKEN") != "event-token" || request.Header.Get("Authorization") != "" {
				t.Fatalf("bad enrollment request: %s headers=%v", request.URL, request.Header)
			}
			return packetResponse(http.StatusCreated, `{"schemaVersion":1,"relayId":"66666666-6666-4666-8666-666666666666","apiBaseUrl":"https://app.chrono.events/api/packet-issuance/v1/relays/66666666-6666-4666-8666-666666666666","scopeId":"site:22222222-2222-4222-8222-222222222222:621632","eventId":"621632","deskInstanceId":"55555555-5555-4555-8555-555555555555","expiresAt":"`+expires+`","receiver":{"bootstrap":true,"operations":true,"feed":true}}`), nil
		}
		if request.Header.Get("Authorization") != "Bearer AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" ||
			request.Header.Get("X-SYNC-TOKEN") != "" || !strings.HasSuffix(request.URL.Path, "/bootstrap") {
			t.Fatalf("bad bootstrap request: %s headers=%v", request.URL, request.Header)
		}
		return packetResponse(http.StatusOK, `{"schemaVersion":1,"scopeId":"site:22222222-2222-4222-8222-222222222222:621632","sourceKind":"site","event":{"id":"621632","name":"Тест","date":"2026-09-13"},"races":[{"id":"100","name":"5 км"}],"registrations":[{"id":"700","eventId":"621632","raceId":"100","bib":"007","epc":"ABC","person":{"id":"member-person:700","firstName":"Анна","lastName":"Тест","birthDate":"1990-03-02","gender":"female","team":"","city":""},"reserve":false,"issued":false,"status":"registered","transferredTo":null,"hasTimingEvidence":false}],"baselineId":"snapshot:test"}`), nil
	})}

	descriptor, err := EnrollPacketRelay(context.Background(), "https://app.chrono.events", "event-token", "621632",
		"55555555-5555-4555-8555-555555555555", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "Chrono Desk")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := FetchPacketBootstrap(context.Background(), descriptor, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || bootstrap.Event.ID != "621632" || len(bootstrap.Registrations) != 1 || bootstrap.Registrations[0].Bib != "007" {
		t.Fatalf("bootstrap=%+v requests=%d", bootstrap, requests)
	}
}

func TestPacketRelayRejectsHTTPAndServerSelectedUnsafeEndpoint(t *testing.T) {
	if _, err := EnrollPacketRelay(context.Background(), "http://chrono.example", "token", "1",
		"55555555-5555-4555-8555-555555555555", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "Desk"); err == nil {
		t.Fatal("HTTP enrollment was accepted")
	}
	previous := syncHTTPClient
	t.Cleanup(func() { syncHTTPClient = previous })
	expires := time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05.000Z")
	syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(*http.Request) (*http.Response, error) {
		return packetResponse(http.StatusCreated, `{"schemaVersion":1,"relayId":"66666666-6666-4666-8666-666666666666","apiBaseUrl":"http://attacker.invalid/relays/66666666-6666-4666-8666-666666666666","scopeId":"site:a:1","eventId":"1","deskInstanceId":"55555555-5555-4555-8555-555555555555","expiresAt":"`+expires+`","receiver":{"bootstrap":true,"operations":true,"feed":true}}`), nil
	})}
	if _, err := EnrollPacketRelay(context.Background(), "https://chrono.example", "token", "1",
		"55555555-5555-4555-8555-555555555555", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "Desk"); err == nil {
		t.Fatal("unsafe relay endpoint was accepted")
	}
}

func TestPacketRelayActionURLRejectsUnboundedOrAmbiguousAddresses(t *testing.T) {
	valid, err := packetRelayActionURL(" https://app.chrono.events/api/packet-issuance/v1/relays/test/ ", "/feed")
	if err != nil || valid != "https://app.chrono.events/api/packet-issuance/v1/relays/test/feed" {
		t.Fatalf("valid URL = %q, err=%v", valid, err)
	}
	for _, value := range []string{
		"", "http://app.chrono.events/relay", "https://user@app.chrono.events/relay",
		"https://app.chrono.events/relay?next=other", "https://app.chrono.events/relay#other",
		"https://app.chrono.events/\x00relay", "https://app.chrono.events/" + strings.Repeat("a", maxPacketRelayURLBytes),
	} {
		if _, err := packetRelayActionURL(value, "/feed"); err == nil {
			t.Fatalf("unsafe packet relay URL accepted: %q", value)
		}
	}
}

func TestConnectPacketIssuanceSitePersistsGrantBeforeInstallingRoster(t *testing.T) {
	previous := syncHTTPClient
	t.Cleanup(func() { syncHTTPClient = previous })
	manager, err := NewEventManager(t.TempDir(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	fixture, err := os.Open("testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ImportExport(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	fixture.Close()
	store, err := manager.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSyncConfig(context.Background(), "ev-100", "https://app.chrono.events", "event-token"); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	bootstrap := packetissuance.Bootstrap{
		SchemaVersion: 2, ScopeID: "site:authority:ev-100", SourceKind: "site", FeedCursor: "7",
		Event: packetissuance.Event{ID: "ev-100", Name: "Test Marathon", Date: "2026-06-07"},
		Races: []packetissuance.Race{{ID: "race-10k", Name: "10 km"}}, BaselineID: "snapshot:test",
		Registrations: []packetissuance.Registration{
			{ID: "mem-1", EventID: "ev-100", RaceID: "race-10k", Bib: "101", EPC: "E280AAA", Person: &packetissuance.Person{ID: "member-person:mem-1", FirstName: "Ivan", LastName: "Petrov", BirthDate: "1990-05-01", Gender: "male", City: "Moscow"}, Status: "registered"},
			{ID: "mem-2", EventID: "ev-100", RaceID: "race-10k", Bib: "102", EPC: "E280BBB", Person: &packetissuance.Person{ID: "member-person:mem-2", FirstName: "Anna", LastName: "Ivanova", Gender: "female"}, Status: "dns"},
		},
	}
	bootstrapJSON, _ := json.Marshal(bootstrap)
	syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/packet-issuance-relays") {
			var enrollment map[string]any
			_ = json.NewDecoder(request.Body).Decode(&enrollment)
			deskID, _ := enrollment["deskInstanceId"].(string)
			descriptor, _ := json.Marshal(map[string]any{
				"schemaVersion": 1, "relayId": "66666666-6666-4666-8666-666666666666",
				"apiBaseUrl": "https://app.chrono.events/api/packet-issuance/v1/relays/66666666-6666-4666-8666-666666666666",
				"scopeId":    "site:authority:ev-100", "eventId": "ev-100", "deskInstanceId": deskID,
				"expiresAt": expires, "receiver": map[string]bool{"bootstrap": true, "operations": true, "feed": true},
			})
			return packetResponse(http.StatusCreated, string(descriptor)), nil
		}
		if strings.HasSuffix(request.URL.Path, "/bootstrap") && strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
			return packetResponse(http.StatusOK, string(bootstrapJSON)), nil
		}
		return packetResponse(http.StatusNotFound, "unexpected"), nil
	})}

	status, err := ConnectPacketIssuanceSite(context.Background(), manager, "ev-100", "Chrono Desk")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListPacketRegistrations(context.Background(), "ev-100")
	if err != nil || len(rows) != 2 || !status.RosterInstalled || status.RelayID == "" || status.FeedCursor != "7" {
		t.Fatalf("status=%+v rows=%d err=%v", status, len(rows), err)
	}
	scope, err := store.GetPacketIssuanceScope(context.Background(), "ev-100")
	if err != nil || scope.SiteFeedCursor != "7" {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
	stored, found, err := manager.GetPacketRelay(context.Background(), "ev-100")
	if err != nil || !found || stored.Credential == "" || stored.RelayID != status.RelayID {
		t.Fatalf("relay=%+v found=%t err=%v", stored, found, err)
	}
	if bytes.Contains(bootstrapJSON, []byte(stored.Credential)) {
		t.Fatal("credential leaked into bootstrap")
	}
}

func TestPushPacketOperationsAcceptsOnlyOrderedMatchingReceipts(t *testing.T) {
	fixture, err := os.ReadFile("../packetissuance/testdata/packet-issuance-operations-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Operation json.RawMessage `json:"operation"`
	}
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	operation, err := packetissuance.ParseOperation(document.Operation)
	if err != nil {
		t.Fatal(err)
	}
	previous := syncHTTPClient
	t.Cleanup(func() { syncHTTPClient = previous })
	syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer relay-secret" || request.Header.Get("X-SYNC-TOKEN") != "" {
			t.Fatalf("wrong operation authorization: %v", request.Header)
		}
		return packetResponse(http.StatusOK, `{"schemaVersion":1,"receipts":[{"operationId":"`+
			operation.OperationID+`","contentHash":"`+packetissuance.ContentHash(operation)+
			`","outcome":"applied","known":false,"code":null}]}`), nil
	})}
	receipts, err := PushPacketOperations(context.Background(), "https://app.chrono.events/api/packet-issuance/v1/relays/test",
		"relay-secret", []packetissuance.Operation{operation})
	if err != nil || len(receipts) != 1 || receipts[0].OperationID != operation.OperationID {
		t.Fatalf("receipts=%+v err=%v", receipts, err)
	}

	syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(*http.Request) (*http.Response, error) {
		return packetResponse(http.StatusOK, `{"schemaVersion":1,"receipts":[{"operationId":"`+
			operation.OperationID+`","contentHash":"sha256:wrong","outcome":"applied","known":true,"code":null}]}`), nil
	})}
	if _, err := PushPacketOperations(context.Background(), "https://app.chrono.events/api/packet-issuance/v1/relays/test",
		"relay-secret", []packetissuance.Operation{operation}); err == nil {
		t.Fatal("mismatched receipt was accepted")
	}
}

func TestPullPacketFeedUsesRelayCredentialCursorAndStrictFixture(t *testing.T) {
	raw, err := os.ReadFile("../packetissuance/testdata/packet-issuance-feed-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	previous := syncHTTPClient
	t.Cleanup(func() { syncHTTPClient = previous })
	syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/packet-issuance/v1/relays/relay/feed" ||
			request.URL.Query().Get("after") != "16" || request.URL.Query().Get("limit") != "100" ||
			request.Header.Get("Authorization") != "Bearer relay-secret" || request.Header.Get("X-SYNC-TOKEN") != "" {
			t.Fatalf("bad feed request: %s headers=%v", request.URL, request.Header)
		}
		return packetResponse(http.StatusOK, string(raw)), nil
	})}
	page, err := PullPacketFeedPage(context.Background(),
		"https://app.chrono.events/api/packet-issuance/v1/relays/relay", "relay-secret",
		"site:22222222-2222-4222-8222-222222222222:621632", "16")
	if err != nil || len(page.Actions) != 2 || page.Cursor.Next != "18" {
		t.Fatalf("page=%+v err=%v", page, err)
	}

	syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(*http.Request) (*http.Response, error) {
		return packetResponse(http.StatusOK, `{"schemaVersion":1,"scopeId":"wrong","cursor":{"after":"16","next":"16","head":"16","hasMore":false},"actions":[]}`), nil
	})}
	if _, err := PullPacketFeedPage(context.Background(),
		"https://app.chrono.events/api/packet-issuance/v1/relays/relay", "relay-secret",
		"site:22222222-2222-4222-8222-222222222222:621632", "16"); err == nil {
		t.Fatal("mismatched feed scope was accepted")
	}
}

func TestPullPacketFeedRecognizesOnlyStrictCursorRecoveryResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        error
	}{
		{name: "expired message", status: http.StatusConflict, contentType: "application/json", body: `{"message":"cursor_expired"}`, want: ErrPacketSiteFeedCursorExpired},
		{name: "ahead error", status: http.StatusConflict, contentType: "application/json; charset=utf-8", body: `{"error":"cursor_ahead"}`, want: ErrPacketSiteFeedCursorAhead},
		{name: "additional field", status: http.StatusConflict, contentType: "application/json", body: `{"message":"cursor_expired","detail":"private"}`},
		{name: "wrong status", status: http.StatusBadRequest, contentType: "application/json", body: `{"message":"cursor_expired"}`},
		{name: "wrong content type", status: http.StatusConflict, contentType: "text/html", body: `{"message":"cursor_expired"}`},
		{name: "unknown code", status: http.StatusConflict, contentType: "application/json", body: `{"message":"try_again"}`},
		{name: "oversized", status: http.StatusConflict, contentType: "application/json", body: strings.Repeat(" ", 4096) + `{"message":"cursor_expired"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := syncHTTPClient
			t.Cleanup(func() { syncHTTPClient = previous })
			syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(*http.Request) (*http.Response, error) {
				response := packetResponse(test.status, test.body)
				response.Header.Set("Content-Type", test.contentType)
				return response, nil
			})}
			_, err := PullPacketFeedPage(context.Background(),
				"https://app.chrono.events/api/packet-issuance/v1/relays/relay", "relay-secret", "scope", "0")
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if test.want == nil && (errors.Is(err, ErrPacketSiteFeedCursorAhead) || errors.Is(err, ErrPacketSiteFeedCursorExpired)) {
				t.Fatalf("unsafe recovery admitted: %v", err)
			}
		})
	}
}

func TestSyncPacketFeedRebasesOnlyAfterAuthenticatedCursorExpiry(t *testing.T) {
	previous := syncHTTPClient
	t.Cleanup(func() { syncHTTPClient = previous })
	manager, err := NewEventManager(t.TempDir(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	fixture, err := os.Open("testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ImportExport(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	fixture.Close()
	row := packetissuance.Registration{ID: "mem-1", EventID: "ev-100", RaceID: "race-10k", Bib: "101", EPC: "E280AAA",
		Person: &packetissuance.Person{ID: "member-person:mem-1", FirstName: "Ivan", LastName: "Petrov", BirthDate: "1990-05-01", Gender: "male", City: "Moscow"},
		Status: "registered"}
	store, err := manager.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InstallPacketIssuanceRoster(context.Background(), sqlite.PacketIssuanceScope{
		EventID: "ev-100", ScopeID: "site:authority:ev-100", BaselineID: "snapshot:old", SourceKind: "site", SiteFeedCursor: "7",
	}, []packetissuance.Registration{row, {
		ID: "mem-2", EventID: "ev-100", RaceID: "race-10k", Bib: "102", EPC: "E280BBB",
		Person: &packetissuance.Person{ID: "member-person:mem-2", FirstName: "Anna", LastName: "Ivanova", Gender: "female"}, Status: "dns",
	}}); err != nil {
		t.Fatal(err)
	}
	state, err := manager.PreparePacketRelay(context.Background(), "ev-100", "https://app.chrono.events")
	if err != nil {
		t.Fatal(err)
	}
	state.RelayID = "66666666-6666-4666-8666-666666666666"
	state.APIBaseURL = "https://app.chrono.events/api/packet-issuance/v1/relays/66666666-6666-4666-8666-666666666666"
	state.ScopeID = "site:authority:ev-100"
	state.ExpiresAt = time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05.000Z")
	if err := manager.CompletePacketRelay(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	row.Person.City = "Engels"
	snapshot := packetissuance.Bootstrap{SchemaVersion: 2, ScopeID: state.ScopeID, SourceKind: "site", FeedCursor: "20",
		Event: packetissuance.Event{ID: "ev-100", Name: "Test Marathon", Date: "2026-06-07"},
		Races: []packetissuance.Race{{ID: "race-10k", Name: "10 km"}}, Registrations: []packetissuance.Registration{row, {
			ID: "mem-2", EventID: "ev-100", RaceID: "race-10k", Bib: "102", EPC: "E280BBB",
			Person: &packetissuance.Person{ID: "member-person:mem-2", FirstName: "Anna", LastName: "Ivanova", Gender: "female"}, Status: "dns",
		}}, BaselineID: "snapshot:new"}
	snapshotJSON, _ := json.Marshal(snapshot)
	requests := 0
	syncHTTPClient = &http.Client{Transport: packetRoundTrip(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer "+state.Credential {
			t.Fatalf("missing relay credential: %v", request.Header)
		}
		if strings.HasSuffix(request.URL.Path, "/feed") {
			if request.URL.Query().Get("after") != "7" {
				t.Fatalf("cursor=%s", request.URL.Query().Get("after"))
			}
			response := packetResponse(http.StatusConflict, `{"message":"cursor_expired"}`)
			response.Header.Set("Content-Type", "application/json")
			return response, nil
		}
		if strings.HasSuffix(request.URL.Path, "/bootstrap") {
			return packetResponse(http.StatusOK, string(snapshotJSON)), nil
		}
		return packetResponse(http.StatusNotFound, "unexpected"), nil
	})}
	result, err := SyncPacketFeed(context.Background(), manager, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListPacketRegistrations(context.Background(), "ev-100")
	scope, _ := store.GetPacketIssuanceScope(context.Background(), "ev-100")
	if requests != 2 || !result.Rebased || result.Cursor != "20" || scope.SiteFeedCursor != "20" ||
		rows[0].Person.City != "Engels" {
		t.Fatalf("requests=%d result=%+v scope=%+v rows=%+v", requests, result, scope, rows)
	}
}

func packetResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
