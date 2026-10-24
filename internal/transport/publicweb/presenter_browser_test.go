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
)

// Opt-in real Chromium test: no reader connections or real event files.
func TestPresenterBrowser(t *testing.T) {
	browser := os.Getenv("CHRONO_PRESENTER_BROWSER")
	if browser == "" {
		t.Skip("set CHRONO_PRESENTER_BROWSER to run three-client tablet smoke")
	}
	s := newServer(t)
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
		_, err = store.DB().Exec(`INSERT INTO members(id,event_id,race_id,number,first_name,last_name,status,finish_time_ms,clean_time,gender,start_time_ms) VALUES(?,'ev-100','race-10k',?,'Тест','Новый',0,?,'00:21:00','male',1000) ON CONFLICT(id) DO UPDATE SET clean_time='00:20:59',last_name='Исправлен'`, fmt.Sprintf("probe-%d", number), number, time.Now().UnixMilli())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fixture/revoke", func(w http.ResponseWriter, r *http.Request) {
		_, err := store.DB().Exec(`UPDATE members SET finish_time_ms=NULL,clean_time=NULL WHERE id=?`, "probe-"+r.URL.Query().Get("number"))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fixture/switch", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.publishedID = "ev-200"
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
