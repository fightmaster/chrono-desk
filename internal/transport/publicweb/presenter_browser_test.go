package publicweb

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Opt-in real Chromium test: no reader connections or real event files.
func TestPresenterBrowser(t *testing.T) {
	browser := os.Getenv("CHRONO_PRESENTER_BROWSER")
	if browser == "" {
		t.Skip("set CHRONO_PRESENTER_BROWSER to run three-client tablet smoke")
	}
	s := newServer(t)
	s.streamHeartbeat = 50 * time.Millisecond
	seedFinishes(t, s, 60)
	store, _ := s.events.Open("ev-100")
	if _, err := store.DB().Exec(`UPDATE races SET category_excludes_top_by_gender=0; UPDATE members SET category_id='cat-m18' WHERE id LIKE 'presenter-%'`); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture = []byte(strings.ReplaceAll(strings.ReplaceAll(string(fixture), "ev-100", "ev-200"), "race-10k", "race-20k"))
	if _, err = s.events.ImportExport(context.Background(), bytes.NewReader(fixture)); err != nil {
		t.Fatal(err)
	}
	other, err := s.events.Open("ev-200")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.DB().Exec(`UPDATE members SET status=0,finish_time_ms=999999,clean_time='00:11:00',last_name='Другое событие' WHERE id='mem-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB().Exec(`INSERT INTO races(id,event_id,name,format) VALUES('race-empty','ev-100','Empty','FixedDistance')`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", s.routes())
	mux.HandleFunc("POST /fixture/finish", func(w http.ResponseWriter, r *http.Request) {
		number, err := strconv.Atoi(r.URL.Query().Get("number"))
		if err != nil {
			http.Error(w, "number", 400)
			return
		}
		id := fmt.Sprintf("probe-%d", number)
		if member, getErr := store.GetMember(r.Context(), id); getErr == nil {
			clean := "00:20:59"
			member.LastName = "Исправлен"
			member.CleanTime = &clean
			err = store.UpsertMember(r.Context(), member)
		} else {
			finish, start, bib := time.Now().UnixMilli(), int64(1000), int64(number)
			clean, gender := "00:21:00", "male"
			err = store.UpsertMember(r.Context(), domain.Member{ID: id, EventID: "ev-100", RaceID: "race-10k", Number: &bib, FirstName: "Тест", LastName: "Новый", FinishTimeMs: &finish, CleanTime: &clean, Gender: &gender, StartTimeMs: &start})
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("X-Fixture-Commit-Ms", strconv.FormatInt(time.Now().UnixMilli(), 10))
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fixture/revoke", func(w http.ResponseWriter, r *http.Request) {
		_, err := store.UpdateEntityField(r.Context(), "members", "finish_time_ms", "probe-"+r.URL.Query().Get("number"), nil)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fixture/club", func(w http.ResponseWriter, r *http.Request) {
		club := "Обновлённый клуб"
		if _, err := store.ApplyMemberClubs(r.Context(), "ev-100", map[string]*string{"probe-" + r.URL.Query().Get("number"): &club}); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fixture/switch", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.setPublishedLocked("ev-200")
		s.mu.Unlock()
		w.WriteHeader(204)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	writes := 0
	started := time.Now()
	stop := make(chan struct{})
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				i++
				_, err := store.DB().Exec(`INSERT INTO rfid_logs(id,event_id,time_ms,board,epc) VALUES(?,'ev-100',?,'fixture','test')`, fmt.Sprintf("load-%d", i), time.Now().UnixMilli())
				if err != nil {
					errs <- err
					return
				}
				writes++
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
		t.Logf("concurrent synthetic observation inserts: %d over %.2fs (%.1f/s; target 100/s)", writes, time.Since(started).Seconds(), float64(writes)/time.Since(started).Seconds())
		select {
		case err := <-errs:
			t.Error(err)
		default:
		}
	}()
	cmd := exec.Command("node", "../../../frontend/scripts/presenter-smoke.mjs", ts.URL)
	cmd.Env = append(os.Environ(), "CHRONO_DESK_BROWSER="+browser)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
