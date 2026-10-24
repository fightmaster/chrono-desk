package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestPresenterClubRefreshIsMetadataOnlyAndAbsencePreservesIt(t *testing.T) {
	body := `{"schema_version":3,"event":{"id":"ev-100"},"members":[{"id":"mem-1","club":"Running club","team":"Wrong team","first_name":"Wrong name","finish_time":"1900-01-01"},{"id":"unknown","club":"Not a registration"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-SYNC-TOKEN") != "token" {
			t.Error("missing scoped token")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	events, err := NewEventManager(t.TempDir(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	fixture, err := os.ReadFile("testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = events.ImportExport(context.Background(), bytes.NewReader(fixture)); err != nil {
		t.Fatal(err)
	}
	store, err := events.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = store.SetSyncConfig(ctx, "ev-100", server.URL, "token"); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListMembersFullByEvent(ctx, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	revisions, _ := store.LiveFinishRankRevision(ctx, "ev-100")
	stats, err := events.RefreshPresenterClubs(ctx, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Provided != 2 || stats.Updated != 1 || stats.Populated != 1 {
		t.Fatal("wrong bounded metadata update count", stats)
	}
	after, _ := store.ListMembersFullByEvent(ctx, "ev-100")
	for i := range after {
		if after[i].ID == "mem-1" && (after[i].Club == nil || *after[i].Club != "Running club") {
			t.Fatal("Club not exposed to member readers")
		}
		after[i].Club = before[i].Club // Compare every protected registration/timing field separately from new affiliation.
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("metadata refresh rewrote members")
	}
	next, _ := store.LiveFinishRankRevision(ctx, "ev-100")
	if revisions != next {
		t.Fatal("club metadata invalidated timing/ranking")
	}
	clubs, _ := store.MemberClubs(ctx, "ev-100")
	if len(clubs) != 1 || clubs["mem-1"] == nil || *clubs["mem-1"] != "Running club" {
		t.Fatal(clubs)
	}
	body = `{"schema_version":5,"event":{"id":"ev-100"},"members":[{"id":"mem-1"}]}`
	stats, err = events.RefreshPresenterClubs(ctx, "ev-100")
	if err != nil || stats.Updated != 0 || stats.Populated != 1 {
		t.Fatal(stats, err)
	}
	clubs, _ = store.MemberClubs(ctx, "ev-100")
	if clubs["mem-1"] == nil {
		t.Fatal("old payload cleared Club")
	}
	body = `{"schema_version":3,"event":{"id":"other"},"members":[{"id":"mem-1","club":null}]}`
	if _, err = events.RefreshPresenterClubs(ctx, "ev-100"); err == nil {
		t.Fatal("cross-event metadata accepted")
	}
	body = `{"schema_version":3,"event":{"id":"ev-100"},"members":[{"id":"mem-1","club":null}]}`
	if stats, err = events.RefreshPresenterClubs(ctx, "ev-100"); err != nil || stats.Populated != 0 {
		t.Fatal("explicit clear did not update nonempty count", stats, err)
	}
	clubs, _ = store.MemberClubs(ctx, "ev-100")
	value, exists := clubs["mem-1"]
	if !exists || value != nil {
		t.Fatal("explicit null clear not retained")
	}
}

func TestClubMetadataRoundTripsWithoutChangingPacketPersonState(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	ctx := context.Background()
	if _, err := store.DB().Exec(`INSERT INTO packet_issuance_scopes(event_id,scope_id,baseline_id,source_kind,installed_at) VALUES('ev-100','scope','baseline','site',1)`); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	rows := document["members"].([]any)
	rows[0].(map[string]any)["club"] = "Independent club"
	rows[0].(map[string]any)["club_person_id"] = "person-1"
	canonical := `{"id":"mem-1","eventId":"ev-100","raceId":"race-10k","person":{"id":"person-1"}}`
	if _, err = store.DB().Exec(`INSERT INTO packet_issuance_registrations(registration_id,event_id,value_json,heads_json,deleted) VALUES('mem-1','ev-100',?,'[]',0)`, canonical); err != nil {
		t.Fatal(err)
	}

	encoded, _ := json.Marshal(document)
	export, err := ParseEventExport(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewEventImporter(store).Import(ctx, export); err != nil {
		t.Fatal(err)
	}
	clubs, _ := store.MemberClubs(ctx, "ev-100")
	if clubs["mem-1"] == nil || *clubs["mem-1"] != "Independent club" {
		t.Fatal("active packet scope lost club metadata")
	}
	backup, _, err := BuildEventExport(ctx, store, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseEventExport(bytes.NewReader(backup))
	if err != nil {
		t.Fatal(err)
	}
	data, err := buildImportData(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if data.Clubs["mem-1"] == nil || *data.Clubs["mem-1"] != "Independent club" {
		t.Fatal("backup lost club metadata")
	}
	original, err := ParseEventExport(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewEventImporter(store).Import(ctx, original); err != nil {
		t.Fatal(err)
	}
	clubs, _ = store.MemberClubs(ctx, "ev-100")
	if clubs["mem-1"] == nil {
		t.Fatal("legacy import erased club")
	}
}
