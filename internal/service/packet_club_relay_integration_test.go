package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

// This opt-in test executes the actual Laravel candidate in a new synthetic-only
// database. It never loads .env, production credentials or participant exports.
func TestClubRelayActualLaravelLostReplyRestartMoveAndCreation(t *testing.T) {
	root := os.Getenv("RUN5_ISSUANCE_FIXTURE_ROOT")
	if root == "" {
		t.Skip("set RUN5_ISSUANCE_FIXTURE_ROOT to run the isolated cross-project acceptance")
	}
	if _, err := os.Stat(filepath.Join(root, "tests/Support/issuance-http-bootstrap.php")); err != nil {
		t.Fatal(err)
	}
	scratch, err := os.MkdirTemp(os.TempDir(), "stopwatch-issuance-http-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scratch) })
	for _, path := range []string{"cache", "storage/logs", "storage/framework/sessions", "storage/framework/views", "storage/framework/cache/data"} {
		if err := os.MkdirAll(filepath.Join(scratch, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(scratch, "issuance.sqlite"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=" + os.Getenv("PATH"), "TZ=UTC", "APP_ENV=testing", "APP_DEBUG=false", "APP_KEY=base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
		"APP_URL=https://app.chrono.events", "DB_CONNECTION=sqlite", "DB_DATABASE=" + filepath.Join(scratch, "issuance.sqlite"), "DB_FOREIGN_KEYS=true", "CACHE_STORE=file", "CACHE_DRIVER=file", "SESSION_DRIVER=file",
		"MAIL_MAILER=array", "QUEUE_CONNECTION=sync", "LOG_CHANNEL=stderr", "ISSUANCE_HTTP_FIXTURE=synthetic-only", "ISSUANCE_HTTP_FIXTURE_DIR=" + scratch,
		"PACKET_ISSUANCE_ENABLED=true", "PACKET_ISSUANCE_EVENT_IDS=621632", "PACKET_ISSUANCE_AUTHORITY_ID=11111111-1111-4111-8111-111111111111"}
	for _, key := range []string{"CONFIG", "SERVICES", "PACKAGES", "ROUTES", "EVENTS"} {
		environment = append(environment, "APP_"+key+"_CACHE="+filepath.Join(scratch, "cache", strings.ToLower(key)+".php"))
	}
	fixture := func(action string) []byte {
		t.Helper()
		command := exec.Command("php", filepath.Join(root, "tests/Support/issuance-http-fixture.php"), action)
		command.Dir = root
		command.Env = environment
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			t.Fatalf("synthetic fixture %s failed: %v: %s", action, err, stderr.String())
		}
		return output
	}
	fixture("setup")
	var seed struct {
		SyncToken string          `json:"syncToken"`
		Export    json.RawMessage `json:"export"`
	}
	if err = json.Unmarshal(fixture("club-relay-export"), &seed); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	serverLog, err := os.Create(filepath.Join(scratch, "php-server.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverLog.Close() })
	server := exec.Command("php", "-S", address, filepath.Join(root, "tests/Support/issuance-http-router.php"))
	server.Dir = root
	server.Env = environment
	server.Stdout = serverLog
	server.Stderr = serverLog
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })
	loopClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		response, err := loopClient.Get("http://" + address + "/__fixture-health")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) == "synthetic-issuance-ready" {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("synthetic PHP server did not become ready")
	}
	previous := syncHTTPClient
	t.Cleanup(func() { syncHTTPClient = previous })
	dropped := false
	metadataRequests := []string{}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	syncHTTPClient = &http.Client{Timeout: 10 * time.Second, Transport: packetRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "app.chrono.events" {
			return nil, fmt.Errorf("refuse non-fixture destination")
		}
		forwarded := request.Clone(request.Context())
		endpoint := *request.URL
		endpoint.Scheme = "http"
		endpoint.Host = address
		forwarded.URL = &endpoint
		forwarded.Host = "app.chrono.events"
		var operation PacketClubOperation
		isClub := request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/club-affiliations")
		if isClub {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			request.Body.Close()
			if json.Unmarshal(body, &operation) != nil {
				return nil, errors.New("invalid synthetic Club request")
			}
			metadataRequests = append(metadataRequests, operation.OperationID)
			forwarded.Body = io.NopCloser(bytes.NewReader(body))
		}
		response, err := transport.RoundTrip(forwarded)
		if err == nil && isClub && !dropped {
			dropped = true
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			return nil, errors.New("synthetic metadata reply lost after commit")
		}
		return response, err
	})}
	ctx := context.Background()
	directory := t.TempDir()
	events, err := NewEventManager(directory, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	if _, err = events.ImportExport(ctx, bytes.NewReader(seed.Export)); err != nil {
		t.Fatal(err)
	}
	store, err := events.Open("621632")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetSyncConfig(ctx, "621632", "https://app.chrono.events", seed.SyncToken); err != nil {
		t.Fatal(err)
	}
	status, err := ConnectPacketIssuanceSite(ctx, events, "621632", "Synthetic club relay")
	if err != nil {
		t.Fatal(err)
	}
	connection := PacketConnectionContext{ConnectionID: "22222222-2222-4222-8222-222222222222", EventID: "621632", ScopeID: status.ScopeID, OriginInstanceID: "33333333-3333-4333-8333-333333333333"}
	scope, err := store.GetPacketIssuanceScope(ctx, "621632")
	if err != nil {
		t.Fatal(err)
	}
	clubEdit := func(id, registrationID, personID, club string) []byte {
		t.Helper()
		person, _ := json.Marshal(personID)
		value, _ := json.Marshal(club)
		body, _ := json.Marshal(PacketClubOperation{SchemaVersion: 1, OperationID: id, ScopeID: status.ScopeID, OriginInstanceID: connection.OriginInstanceID, RegistrationID: registrationID, PersonID: person, Club: value})
		receipt, err := ApplyPacketClubAffiliation(ctx, store, connection, body)
		if err != nil || receipt.Outcome != "applied" {
			t.Fatalf("tablet Club receipt: %+v %v", receipt, err)
		}
		return body
	}
	const firstClub = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	originalEdit := clubEdit(firstClub, "700", "member-person:700", "Edited before move")
	receiveOperation := func(id string, sequence int64, commandData map[string]any) {
		t.Helper()
		encoded, _ := json.Marshal(commandData)
		var command packetissuance.Command
		if err = json.Unmarshal(encoded, &command); err != nil {
			t.Fatal(err)
		}
		var selected []packetissuance.Registration
		var bases []packetissuance.Base
		for _, field := range []string{"registrationId", "targetId"} {
			value, exists := commandData[field]
			if !exists {
				continue
			}
			registrationID := value.(string)
			if command.Type == "create_registration" {
				row, err := packetissuance.EmptyRegistration(command)
				if err != nil {
					t.Fatal(err)
				}
				selected = append(selected, row)
				bases = append(bases, packetissuance.Base{RegistrationID: registrationID, Heads: []string{}})
				continue
			}
			row, err := store.FindPacketRegistration(ctx, "621632", registrationID)
			if err != nil || !row.Found {
				t.Fatal("missing selected row", err)
			}
			selected = append(selected, row.Value)
			bases = append(bases, packetissuance.Base{RegistrationID: registrationID, Heads: row.Heads})
		}
		changes, err := packetissuance.Apply(selected, command)
		if err != nil {
			t.Fatal(err)
		}
		envelope := map[string]any{"schemaVersion": 1, "operationId": id, "scopeId": status.ScopeID, "baselineId": scope.BaselineID, "originInstanceId": connection.OriginInstanceID, "originSequence": sequence, "createdAtMs": int64(1789290000000) + sequence, "claimedActor": "Synthetic volunteer", "command": commandData, "bases": bases, "changes": changes}
		raw, _ := json.Marshal(envelope)
		operation, err := packetissuance.ParseOperation(raw)
		if err != nil {
			t.Fatal(err)
		}
		receipts, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, []packetissuance.Operation{operation})
		if err != nil || len(receipts) != 1 || receipts[0].Outcome != "applied" {
			t.Fatalf("tablet operation receipt: %+v %v", receipts, err)
		}
	}
	receiveOperation("44444444-4444-4444-8444-444444444444", 1, map[string]any{"type": "assign_reserve", "registrationId": "700", "targetId": "703", "issuePacket": true, "returnSource": false})
	const createdID = "123456789012345"
	const createdPerson = "local:55555555-5555-4555-8555-555555555555"
	receiveOperation("55555555-5555-4555-8555-555555555555", 2, map[string]any{"type": "create_registration", "registrationId": createdID, "eventId": "621632", "raceId": "100", "bib": "131", "issuePacket": true, "person": map[string]any{"id": createdPerson, "firstName": "Synthetic", "lastName": "Walkin", "birthDate": "2000-01-01", "gender": "male", "team": "Relay", "city": "City"}})
	clubEdit("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", createdID, createdPerson, "New Club")
	if _, err = SyncPacketOperations(ctx, events, "621632"); err == nil {
		t.Fatal("lost metadata reply must retain the canonical queue")
	}
	type backendState struct {
		Clubs    map[string]*string `json:"clubs"`
		Receipts []struct {
			ID     string `json:"operation_id"`
			Origin string `json:"origin_instance_id"`
			Kind   string `json:"receiver_kind"`
		} `json:"receipts"`
		Operations int `json:"operations"`
		Raw        int `json:"raw"`
	}
	readBackend := func() backendState {
		t.Helper()
		var state backendState
		if err = json.Unmarshal(fixture("club-relay-snapshot"), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	first := readBackend()
	if first.Operations != 0 || len(first.Receipts) != 1 || first.Clubs["700"] == nil || *first.Clubs["700"] != "Edited before move" {
		t.Fatalf("lost-reply backend state: %+v", first)
	}
	queued, err := store.PendingPacketClubEdits(ctx, "621632")
	if err != nil || len(queued) != 2 {
		t.Fatal(queued, err)
	}
	// Persist a controlled future retry deadline, reopen the actual event database,
	// and prove neither a new UUID nor a later move can bypass the saved backoff.
	if _, err = store.DB().Exec(`UPDATE packet_club_outbox SET next_attempt_at=? WHERE operation_id=?`, time.Now().Add(time.Hour).UnixMilli(), firstClub); err != nil {
		t.Fatal(err)
	}
	events.Close()
	events, err = NewEventManager(directory, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	store, err = events.Open("621632")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := ApplyPacketClubAffiliation(ctx, store, connection, originalEdit)
	if err != nil || !recovered.Known {
		t.Fatal("restart lost tablet receipt", recovered, err)
	}
	if _, err = SyncPacketOperations(ctx, events, "621632"); err == nil || len(metadataRequests) != 1 {
		t.Fatal("restart bypassed Club backoff", err, metadataRequests)
	}
	if _, err = store.DB().Exec(`UPDATE packet_club_outbox SET next_attempt_at=0 WHERE operation_id=?`, firstClub); err != nil {
		t.Fatal(err)
	}
	result, err := SyncPacketOperations(ctx, events, "621632")
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 2 || result.ClubAccepted != 2 || result.ClubReview != 0 {
		t.Fatalf("sync=%+v", result)
	}
	final := readBackend()
	if final.Clubs["700"] != nil || final.Clubs["703"] == nil || *final.Clubs["703"] != "Edited before move" || final.Clubs[createdID] == nil || *final.Clubs[createdID] != "New Club" || len(final.Receipts) != 2 || final.Operations != 2 || final.Raw != 0 {
		t.Fatalf("final real backend state: %+v", final)
	}
	for _, receipt := range final.Receipts {
		if receipt.Origin != connection.OriginInstanceID || receipt.Kind != "relay" {
			t.Fatal("original tablet provenance lost", receipt)
		}
	}
	if strings.Join(metadataRequests, ",") != strings.Join([]string{firstClub, firstClub, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}, ",") {
		t.Fatal("metadata retry identities changed", metadataRequests)
	}
	payload, _, err := BuildSyncPayloadV3(ctx, store, "621632", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var generic struct {
		Edits []map[string]any `json:"member_edits"`
	}
	if json.Unmarshal(payload, &generic) != nil || len(generic.Edits) != 0 {
		t.Fatalf("transported metadata echoed as generic edit: %s", payload)
	}
	counts, err := store.PacketClubDeliveryCounts(ctx, "621632")
	if err != nil || counts.Pending != 0 || counts.Review != 0 {
		t.Fatal(counts, err)
	}
	ownerJSON, _ := json.Marshal("member-person:700")
	if _, err := ApplyEdit(ctx, store, EditRequest{Entity: "member", EntityID: "703", Field: "club", ClubPersonID: ownerJSON, Value: json.RawMessage(`"Native editor Club"`)}); err != nil {
		t.Fatal(err)
	}
	relayStatus, err := GetPacketRelayStatus(ctx, events, "621632")
	if err != nil || relayStatus.ClubPending != 1 {
		t.Fatal("native pending Club not visible", relayStatus, err)
	}
	nativeSync, err := SyncPacketOperations(ctx, events, "621632")
	if err != nil || nativeSync.ClubAccepted != 1 || nativeSync.Attempted != 0 {
		t.Fatal("native edit did not use detached relay", nativeSync, err)
	}
	afterNative := readBackend()
	if afterNative.Clubs["703"] == nil || *afterNative.Clubs["703"] != "Native editor Club" || len(afterNative.Receipts) != 3 {
		t.Fatal("native edit missing at actual site", afterNative)
	}
	foundNative := false
	for _, receipt := range afterNative.Receipts {
		if receipt.Origin == events.InstallationID() {
			foundNative = true
		}
	}
	if !foundNative {
		t.Fatal("native editor authorship was not retained")
	}
	authoritative := "Later site correction"
	fixture("club-relay-site-correction")
	refreshed, err := SyncPacketOperations(ctx, events, "621632")
	if err != nil || refreshed.ClubRefreshDeferred {
		t.Fatal("authoritative metadata refresh failed", refreshed, err)
	}
	if _, err = ReapplyLocalEdits(ctx, store); err != nil {
		t.Fatal(err)
	}
	member, err := store.GetMember(ctx, "703")
	if err != nil || member.Club == nil || *member.Club != authoritative {
		t.Fatal("acknowledged Club replay overwrote authoritative correction", err)
	}
	t.Log("Actual Laravel + Desk: lost reply, restart/backoff, original UUIDs, Club-before-move, create-before-Club, no generic echo and no later replay overwrite passed")
}
