package packetlan

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

type testAdvertisement struct{ stopped bool }

func (a *testAdvertisement) Shutdown() { a.stopped = true }

func TestPacketLANHTTPSExposesOnlyScopedIssuanceAndResumes(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	events, err := service.NewEventManager(directory, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(events.Close)
	fixture, err := os.Open("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := events.ImportExport(ctx, fixture); err != nil {
		fixture.Close()
		t.Fatal(err)
	}
	fixture.Close()
	installPacketRoster(t, events)

	server, err := New(events, log.New(io.Discard, "", 0), directory, DefaultPort,
		"https://www.run5.run/stopwatch/", []string{"https://www.run5.run"})
	if err != nil {
		t.Fatal(err)
	}
	server.port = 0
	advertised := &testAdvertisement{}
	server.advertise = func(int, string) (advertisement, error) { return advertised, nil }
	if err := server.Start(ctx, "ev-100"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Stop(stop)
	})
	status, err := server.Status(ctx, "ev-100")
	if err != nil || !status.Running || status.Port == 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	client := packetLANClient(t, server, status.Port)
	invitation, err := server.CreateInvitation(ctx, "ev-100", "Стол 1")
	if err != nil {
		t.Fatal(err)
	}
	pairing := invitationPayload(t, invitation.InvitationURL)
	base := pairing.APIBaseURL + "/connections/" + pairing.ConnectionID
	credential := strings.Repeat("A", 43)
	claim := map[string]string{"pairingCode": pairing.PairingCode,
		"originInstanceId": "22222222-2222-4222-8222-222222222222", "credential": credential}
	for attempt := 0; attempt < 2; attempt++ {
		response := packetRequest(t, client, http.MethodPost, base+"/claim", claim, "", "https://www.run5.run")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("claim attempt %d returned %d", attempt+1, response.StatusCode)
		}
		response.Body.Close()
	}

	response := packetRequest(t, client, http.MethodGet, base+"/bootstrap", nil, credential, "https://www.run5.run")
	var bootstrap packetissuance.Bootstrap
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&bootstrap) != nil {
		response.Body.Close()
		t.Fatalf("bootstrap returned %d", response.StatusCode)
	}
	response.Body.Close()
	if bootstrap.SchemaVersion != 2 || bootstrap.FeedCursor != "0" || bootstrap.Event.ID != "ev-100" || len(bootstrap.Registrations) != 2 {
		t.Fatalf("bootstrap=%+v", bootstrap)
	}

	response = packetRequest(t, client, http.MethodGet, base+"/feed?after=0&limit=100", nil, credential, "https://www.run5.run")
	var page packetissuance.FeedPage
	feedBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || json.Unmarshal(feedBody, &page) != nil {
		t.Fatalf("feed returned %d: %s", response.StatusCode, feedBody)
	}
	if page.Cursor.After != "0" || page.Cursor.Next != "0" || len(page.Actions) != 0 {
		t.Fatalf("feed=%+v", page)
	}
	response = packetRequest(t, client, http.MethodGet, base+"/feed?after=0&after=1&limit=100", nil, credential, "https://www.run5.run")
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate feed cursor returned %d", response.StatusCode)
	}
	response.Body.Close()
	response = packetRequest(t, client, http.MethodGet, base+"/feed?after=1&limit=100", nil, credential, "https://www.run5.run")
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("future feed cursor returned %d", response.StatusCode)
	}
	response.Body.Close()
	response = packetRequest(t, client, http.MethodGet, base+"/feed?after=0&limit=101", nil, credential, "https://www.run5.run")
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("oversized feed limit returned %d", response.StatusCode)
	}
	response.Body.Close()
	request, err := http.NewRequest(http.MethodPost, base+"/operations", strings.NewReader(`{"schemaVersion":1,"operations":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://www.run5.run")
	request.Header.Set("Authorization", "Bearer "+credential)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("operation without JSON content type returned %d", response.StatusCode)
	}
	response.Body.Close()
	request, err = http.NewRequest(http.MethodOptions, base+"/bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://www.run5.run")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "Authorization, Accept")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("valid preflight returned %d", response.StatusCode)
	}
	response.Body.Close()
	request, _ = http.NewRequest(http.MethodOptions, "https://chrono-desk.local:"+itoa(status.Port)+"/private", nil)
	request.Header.Set("Origin", "https://www.run5.run")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown preflight target returned %d", response.StatusCode)
	}
	response.Body.Close()

	response = packetRequest(t, client, http.MethodGet, "https://chrono-desk.local:"+itoa(status.Port)+"/api/events/ev-100", nil, credential, "https://www.run5.run")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("Desk control route leaked through LAN: %d", response.StatusCode)
	}
	response.Body.Close()
	response = packetRequest(t, client, http.MethodGet, base+"/bootstrap", nil, credential, "https://attacker.invalid")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin returned %d", response.StatusCode)
	}
	response.Body.Close()

	stop, cancel := context.WithTimeout(ctx, time.Second)
	if err := server.Shutdown(stop); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if !advertised.stopped {
		t.Fatal("mDNS advertisement survived shutdown")
	}
	if eventID, enabled, err := events.ActivePacketLAN(ctx); err != nil || !enabled || eventID != "ev-100" {
		t.Fatalf("persisted receiver state event=%s enabled=%t err=%v", eventID, enabled, err)
	}
	server.advertise = func(int, string) (advertisement, error) { return &testAdvertisement{}, nil }
	if err := server.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	stop, cancel = context.WithTimeout(ctx, time.Second)
	if err := server.Stop(stop); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if _, enabled, err := events.ActivePacketLAN(ctx); err != nil || enabled {
		t.Fatalf("explicit stop was not persisted: enabled=%t err=%v", enabled, err)
	}
}

func TestPacketLANClaimLimiterIsBoundedAndResets(t *testing.T) {
	server := &Server{claims: map[string]claimWindow{}}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for attempt := 0; attempt < 10; attempt++ {
		if !server.allowClaim("192.0.2.1:1234", "connection", now) {
			t.Fatalf("attempt %d was rejected", attempt+1)
		}
	}
	if server.allowClaim("192.0.2.1:9999", "connection", now) {
		t.Fatal("eleventh claim was accepted")
	}
	if !server.allowClaim("192.0.2.1:9999", "connection", now.Add(time.Minute)) {
		t.Fatal("claim window did not reset")
	}
}

func installPacketRoster(t *testing.T, events *service.EventService) {
	t.Helper()
	store, err := events.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	rows := []packetissuance.Registration{
		{ID: "mem-1", EventID: "ev-100", RaceID: "race-10k", Bib: "101", EPC: "E280AAA",
			Person: &packetissuance.Person{ID: "member-person:mem-1", FirstName: "Ivan", LastName: "Petrov", BirthDate: "1990-05-01", Gender: "male", City: "Moscow"}, Status: "registered"},
		{ID: "mem-2", EventID: "ev-100", RaceID: "race-10k", Bib: "102", EPC: "E280BBB",
			Person: &packetissuance.Person{ID: "member-person:mem-2", FirstName: "Anna", LastName: "Ivanova", Gender: "female"}, Status: "dns"},
	}
	if err := store.InstallPacketIssuanceRoster(context.Background(), sqlite.PacketIssuanceScope{
		EventID: "ev-100", ScopeID: "site:authority:ev-100", BaselineID: "snapshot:test",
		SourceKind: "site", SiteFeedCursor: "0",
	}, rows); err != nil {
		t.Fatal(err)
	}
}

func packetLANClient(t *testing.T, server *Server, port int) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(server.CACertificate()) {
		t.Fatal("cannot trust packet LAN CA")
	}
	dialer := &net.Dialer{Timeout: time.Second}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", "127.0.0.1:"+itoa(port))
		}}
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func packetRequest(t *testing.T, client *http.Client, method, endpoint string, payload any, credential, origin string) *http.Response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Origin", origin)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

type packetInvitation struct {
	APIBaseURL   string `json:"apiBaseUrl"`
	ConnectionID string `json:"connectionId"`
	PairingCode  string `json:"pairingCode"`
}

func invitationPayload(t *testing.T, value string) packetInvitation {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := url.PathUnescape(strings.TrimPrefix(parsed.Fragment, "issuance-connect="))
	if err != nil {
		t.Fatal(err)
	}
	var invitation packetInvitation
	if err := json.Unmarshal([]byte(raw), &invitation); err != nil {
		t.Fatal(err)
	}
	return invitation
}

func itoa(value int) string { return strconv.Itoa(value) }
