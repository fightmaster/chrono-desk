package publicweb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func seedFinishes(t *testing.T, s *Server, count int) {
	t.Helper()
	store, err := s.events.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB().Exec(`UPDATE members SET finish_time_ms=NULL,clean_time=NULL`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		_, err = store.DB().Exec(`INSERT INTO members(id,event_id,race_id,number,first_name,last_name,team,city,status,finish_time_ms,clean_time,gender,start_time_ms) VALUES(?,'ev-100','race-10k',?,'Иван','Финишировавший','Команда','Город',0,?,'00:20:00','male',1000)`, fmt.Sprintf("presenter-%03d", i), i+1, 100000+i)
		if err != nil {
			t.Fatal(err)
		}
	}
	club := "Беговой клуб"
	if _, err := store.ApplyMemberClubs(context.Background(), "ev-100", map[string]*string{"presenter-059": &club}); err != nil {
		t.Fatal(err)
	}
	s.publishedID = "ev-100"
}

func TestLiveFinishesBoundedPrivateSnapshot(t *testing.T) {
	s := newServer(t)
	seedFinishes(t, s, 60)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	var body publicFinishes
	getJSON(t, ts.URL+"/api/live/finishes", &body)
	if !body.Published || len(body.Rows) != 10 || *body.Rows[0].Number != 60 || *body.Rows[9].Number != 51 {
		t.Fatalf("unexpected snapshot: %+v", body)
	}
	if body.Rows[0].GenderPlace == nil || *body.Rows[0].GenderPlace != 60 {
		t.Fatal("rank was calculated only over the visible ten rows")
	}
	if body.Rows[0].Club == nil || *body.Rows[0].Club != "Беговой клуб" {
		t.Fatal("separate club missing from finish snapshot")
	}
	store, _ := s.events.Open("ev-100")
	if _, err := store.DB().Exec(`UPDATE races SET category_excludes_top_by_gender=0; UPDATE members SET category_id='cat-m18' WHERE id LIKE 'presenter-%'`); err != nil {
		t.Fatal(err)
	}
	getJSON(t, ts.URL+"/api/live/finishes", &body)
	if body.Rows[0].CategoryPlace == nil || *body.Rows[0].CategoryPlace != 60 || body.Rows[0].CategoryName == nil || *body.Rows[0].CategoryName != "M18-39" {
		t.Fatal("group place did not include finishers outside visible ten")
	}
	key := body.Rows[0].Key
	if len(key) != 64 || strings.Contains(key, "presenter") {
		t.Fatalf("nonopaque key %q", key)
	}
	getJSON(t, ts.URL+"/api/live/finishes?race_id=race-10k", &body)
	if len(body.Rows) != 10 || body.Rows[0].Key != key || !body.Filtered {
		t.Fatal("poll/filter changed participation identity")
	}
	response := getJSON(t, ts.URL+"/api/live/finishes?race_id=foreign", nil)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal(response.StatusCode)
	}
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/live/finishes", nil))
	for _, secret := range []string{"member_id", "presenter-", "dob", "epc", "email", "phone"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("exposed %s", secret)
		}
	}
	for _, path := range []string{"/live", "/live.js", "/api/live/finishes"} {
		recorder = httptest.NewRecorder()
		s.routes().ServeHTTP(recorder, httptest.NewRequest("POST", path, nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s: %d", path, recorder.Code)
		}
	}
	s.publishedID = ""
	getJSON(t, ts.URL+"/api/live/finishes", &body)
	if body.Published || len(body.Rows) != 0 {
		t.Fatal("unpublished results exposed")
	}
}

func TestLiveFinishCorrectionsAndExclusions(t *testing.T) {
	s := newServer(t)
	seedFinishes(t, s, 8)
	store, _ := s.events.Open("ev-100")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.DB().Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE members SET status=1 WHERE id='presenter-000'`)
	exec(`UPDATE members SET status=2 WHERE id='presenter-001'`)
	exec(`UPDATE members SET status=3 WHERE id='presenter-002'`)
	exec(`UPDATE members SET finish_time_ms=NULL WHERE id='presenter-003'`)
	exec(`UPDATE members SET clean_time='' WHERE id='presenter-004'`)
	exec(`INSERT INTO packet_issuance_registrations(registration_id,event_id,value_json) VALUES('presenter-005','ev-100','{"reserve":true}')`)
	exec(`INSERT INTO races(id,event_id,name,format) VALUES('limited','ev-100','Hour','TimeLimited')`)
	exec(`UPDATE members SET race_id='limited' WHERE id='presenter-006'`)
	snapshot, err := s.events.LiveFinishes(context.Background(), "ev-100", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Finishes) != 1 || snapshot.Finishes[0].MemberID != "presenter-007" {
		t.Fatalf("unexpected finish policy: %+v", snapshot)
	}
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/live/finishes?race_id=limited", nil))
	if recorder.Code != 400 {
		t.Fatal(recorder.Code)
	}
	exec(`UPDATE members SET clean_time='00:19:59',last_name='Исправлен' WHERE id='presenter-007'`)
	snapshot, err = s.events.LiveFinishes(context.Background(), "ev-100", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Finishes) != 1 || snapshot.Finishes[0].CleanTime != "00:19:59" || snapshot.Finishes[0].LastName != "Исправлен" {
		t.Fatal("correction missing")
	}
	exec(`UPDATE members SET finish_time_ms=NULL,clean_time=NULL WHERE id='presenter-007'`)
	snapshot, err = s.events.LiveFinishes(context.Background(), "ev-100", "")
	if err != nil || len(snapshot.Finishes) != 0 {
		t.Fatal("revoked finish retained", err)
	}
}

func TestLiveFinishReadDoesNotWaitForWriter(t *testing.T) {
	s := newServer(t)
	seedFinishes(t, s, 1)
	store, _ := s.events.Open("ev-100")
	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE members SET last_name='Uncommitted' WHERE id='presenter-000'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	snapshot, err := s.events.LiveFinishes(ctx, "ev-100", "")
	if err != nil {
		t.Fatal("read blocked by writer", err)
	}
	if len(snapshot.Finishes) != 1 || snapshot.Finishes[0].LastName != "Финишировавший" {
		t.Fatal("read uncommitted state")
	}
}
