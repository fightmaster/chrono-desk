package publicweb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

// newServer builds a server backed by the imported contract fixture (event
// ev-100, race race-10k). Returns the server without publishing anything.
func newServer(t *testing.T) *Server {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	events, err := service.NewEventManager(t.TempDir(), logger)
	if err != nil {
		t.Fatalf("event manager: %v", err)
	}
	t.Cleanup(events.Close)

	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := events.ImportExport(context.Background(), bytes.NewReader(fixture)); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	return New(events, logger, 0)
}

func getJSON(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	if v != nil {
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp
}

// The public projection must never leak the date of birth (PII) or internal ids.
func TestProtocolTrimsPII(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	var body struct {
		RaceName string                       `json:"race_name"`
		Rows     []map[string]json.RawMessage `json:"rows"`
	}
	getJSON(t, ts.URL+"/api/races/race-10k", &body)

	if body.RaceName == "" || len(body.Rows) == 0 {
		t.Fatalf("expected a named race with rows, got %+v", body)
	}
	for i, row := range body.Rows {
		if _, ok := row["dob"]; ok {
			t.Errorf("row %d exposes dob — PII must be trimmed", i)
		}
		if _, ok := row["member_id"]; ok {
			t.Errorf("row %d exposes member_id", i)
		}
		if _, ok := row["last_name"]; !ok {
			t.Errorf("row %d missing last_name", i)
		}
	}
}

// /api/races reflects whether anything is being broadcast.
func TestRacesPublishedFlag(t *testing.T) {
	s := newServer(t)
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	var off struct {
		Published bool          `json:"published"`
		Races     []interface{} `json:"races"`
	}
	getJSON(t, ts.URL+"/api/races", &off)
	if off.Published || len(off.Races) != 0 {
		t.Fatalf("before publish = %+v, want published:false no races", off)
	}

	s.publishedID = "ev-100"
	var on struct {
		Published bool `json:"published"`
		Races     []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"races"`
	}
	getJSON(t, ts.URL+"/api/races", &on)
	if !on.Published || len(on.Races) == 0 {
		t.Fatalf("after publish = %+v, want published:true with races", on)
	}
}

// Read-only: a non-GET method on any public route is rejected by the mux.
func TestReadOnly(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	for _, path := range []string{"/api/races", "/api/races/race-10k", "/api/meta"} {
		resp, err := http.Post(ts.URL+path, "application/json", nil)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, resp.StatusCode)
		}
	}
}

// Publish opens the LAN port lazily; Unpublish closes it.
func TestPublishLifecycle(t *testing.T) {
	s := newServer(t)
	s.port = freePort(t)

	if s.Status().Running {
		t.Fatal("running before publish")
	}
	if err := s.Publish("ev-100"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if st := s.Status(); !st.Running || st.EventID != "ev-100" {
		t.Fatalf("status = %+v, want running ev-100", st)
	}

	// The server binds 0.0.0.0, so it also answers on loopback.
	url := "http://127.0.0.1:" + strconv.Itoa(s.port) + "/healthz"
	resp := getJSON(t, url, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", resp.StatusCode)
	}

	s.Unpublish()
	if s.Status().Running {
		t.Fatal("still running after unpublish")
	}
	// Give the listener a moment to close, then confirm it's gone.
	time.Sleep(50 * time.Millisecond)
	if _, err := http.Get(url); err == nil {
		t.Fatal("port still accepts connections after unpublish")
	}
}

func TestIsVirtualIface(t *testing.T) {
	virtual := []string{"docker0", "br-1a2b3c", "veth1234", "virbr0", "vmnet8", "tailscale0", "wg0", "utun3"}
	real := []string{"eth0", "enp3s0", "wlan0", "wlp2s0", "en0", "eno1"}
	for _, n := range virtual {
		if !isVirtualIface(n) {
			t.Errorf("%s should be treated as virtual", n)
		}
	}
	for _, n := range real {
		if isVirtualIface(n) {
			t.Errorf("%s should be treated as a real interface", n)
		}
	}
}

func TestLanURLsAreNonLoopbackIPv4(t *testing.T) {
	for _, u := range lanURLs(8090) {
		if strings.Contains(u, "127.0.0.1") || strings.Contains(u, "[") {
			t.Errorf("lanURLs returned a loopback/IPv6 address: %s", u)
		}
		if !strings.HasSuffix(u, ":8090") {
			t.Errorf("lanURLs dropped the port: %s", u)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
