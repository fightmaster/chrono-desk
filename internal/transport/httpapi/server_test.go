package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func startTestServer(t *testing.T) *Server {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	events, err := service.NewEventManager(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("event manager: %v", err)
	}
	t.Cleanup(events.Close)

	srv, err := New("127.0.0.1:0", events, logger)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go srv.Start() //nolint:errcheck // returns ErrServerClosed on shutdown
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

func TestHealthEndpoint(t *testing.T) {
	srv := startTestServer(t)

	resp, err := http.Get(srv.BaseURL() + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want %q", body["status"], "ok")
	}
}

// End-to-end over HTTP: import the contract fixture, add flash-drive logs,
// recount, read the ranked protocol.
func TestImportRecountProtocolFlow(t *testing.T) {
	srv := startTestServer(t)
	base := srv.BaseURL()

	// 1. Import the event export fixture.
	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	resp := mustPost(t, base+"/api/events/import", "application/json", strings.NewReader(string(fixture)))
	var stats service.ImportStats
	decodeBody(t, resp, &stats)
	if stats.EventID != "ev-100" || stats.Members != 2 {
		t.Fatalf("import stats = %+v", stats)
	}

	// 2. Event shows up in the list with its timezone.
	resp = mustGet(t, base+"/api/events")
	var infos []service.EventInfo
	decodeBody(t, resp, &infos)
	if len(infos) != 1 || infos[0].ID != "ev-100" || infos[0].Timezone != "Europe/Moscow" {
		t.Fatalf("events = %+v", infos)
	}

	// 3. Flash-drive CSVs: a start read from reader U100 and a finish read
	// from reader U659. Race start 2026-06-07T09:00:00+03:00; the finish
	// since-guard opens at 09:10.
	var imp service.FeibotImportResult
	resp = mustPost(t, base+"/api/events/ev-100/rfid-import?device=U100&tz=Europe/Moscow",
		"text/csv", strings.NewReader("E280AAA:2026-06-07_09:00:05.000,port=1,rssi=-60\n"))
	decodeBody(t, resp, &imp)
	if imp.Inserted != 1 {
		t.Fatalf("start import = %+v", imp)
	}
	resp = mustPost(t, base+"/api/events/ev-100/rfid-import?device=U659&tz=Europe/Moscow",
		"text/csv", strings.NewReader("E280AAA:2026-06-07_09:42:10.500,port=2,rssi=-55\n"))
	decodeBody(t, resp, &imp)
	if imp.Inserted != 1 {
		t.Fatalf("finish import = %+v", imp)
	}

	// 4. Recount the event.
	resp = mustPost(t, base+"/api/events/ev-100/recount", "application/json", nil)
	var rstats service.RecountStats
	decodeBody(t, resp, &rstats)
	if rstats.LogsReplayed < 3 {
		t.Fatalf("recount stats = %+v", rstats)
	}

	// 5. Protocol: member 1 finished, member 2 is DNS.
	resp = mustGet(t, base+"/api/events/ev-100/races/race-10k/protocol")
	var protocol service.ProtocolResponse
	decodeBody(t, resp, &protocol)
	if protocol.RaceName != "10 km" || len(protocol.Rows) != 2 {
		t.Fatalf("protocol = %+v", protocol)
	}
	first := protocol.Rows[0]
	if first.MemberID != "mem-1" || first.Place == nil || *first.Place != 1 {
		t.Fatalf("first row = %+v", first)
	}
	// The fixture already carries a finish-board log at 09:10:00 — it wins
	// over the later CSV read (FINISH is set once): 09:10:00 − 09:00:05.
	if first.CleanTime == nil || *first.CleanTime != "00:09:55" {
		got := "<nil>"
		if first.CleanTime != nil {
			got = *first.CleanTime
		}
		t.Fatalf("clean time = %s, want 00:09:55", got)
	}
	if first.CategoryName == nil || *first.CategoryName != "M18-39" {
		t.Fatalf("category name = %v", first.CategoryName)
	}
	second := protocol.Rows[1]
	if second.MemberID != "mem-2" || second.Status != "dns" || second.Place != nil {
		t.Fatalf("second row = %+v", second)
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return requireOK(t, url, resp)
}

func mustPost(t *testing.T, url, contentType string, body io.Reader) *http.Response {
	t.Helper()
	resp, err := http.Post(url, contentType, body)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return requireOK(t, url, resp)
}

func requireOK(t *testing.T, url string, resp *http.Response) *http.Response {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("%s → %d: %s", url, resp.StatusCode, raw)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// Regression: the webview preflights non-simple methods; DELETE must be in
// Access-Control-Allow-Methods or checkpoint deletion dies with "Load failed".
// Feibot CSV is zoneless: importing without a timezone must fail closed, not
// silently assume UTC (which would shift every read by the venue's offset).
func TestRfidImportRequiresTimezone(t *testing.T) {
	srv := startTestServer(t)
	base := srv.BaseURL()

	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	mustPost(t, base+"/api/events/import", "application/json", strings.NewReader(string(fixture)))

	resp, err := http.Post(base+"/api/events/ev-100/rfid-import?device=U659", "text/csv", strings.NewReader(""))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("import without tz returned 200 — must fail closed")
	}
}

func TestCORSAllowsDelete(t *testing.T) {
	srv := startTestServer(t)

	req, err := http.NewRequest(http.MethodOptions, srv.BaseURL()+"/api/events/x/checkpoints/y", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "wails://wails.localhost")
	req.Header.Set("Access-Control-Request-Method", "DELETE")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
	}
	allow := resp.Header.Get("Access-Control-Allow-Methods")
	if !strings.Contains(allow, "DELETE") {
		t.Fatalf("Allow-Methods = %q, must contain DELETE", allow)
	}
}
