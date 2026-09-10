package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func TestEdgeControlAPIRequiresAuthAndExplicitProvisioning(t *testing.T) {
	srv := startTestServer(t)
	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(fixture, []byte("ev-100"), []byte("100"))
	decodeBody(t, mustPost(t, srv.BaseURL()+"/api/events/import", "application/json", bytes.NewReader(fixture)), &map[string]any{})
	base := srv.BaseURL() + "/api/events/100/live/edge"
	request := func(method, path, body string, auth bool) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if auth {
			req.Header.Set("Authorization", "Bearer "+testAPIToken)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	for _, path := range []string{"/config", "/journal"} {
		if code, _ := request("GET", path, "", false); code != 401 {
			t.Fatalf("unprotected %s: %d", path, code)
		}
	}
	if code, _ := request("POST", "/start", `{}`, true); code < 400 {
		t.Fatal("started without binding")
	}
	if code, _ := request("PUT", "/config", `{}`, true); code < 400 {
		t.Fatal("missing bindings treated as deletion")
	}
	if code, _ := request("PUT", "/config", `{"bindings":[{"board":"unregistered","source_session_id":"one"}]}`, true); code < 400 {
		t.Fatal("unknown checkpoint board allowed")
	}
	config := `{"bindings":[{"board":"Feibot:U659","source_session_id":"one"}]}`
	if code, _ := request("PUT", "/config", config, false); code != 401 {
		t.Fatal("unprotected settings")
	}
	if code, body := request("PUT", "/config", config, true); code != 200 {
		t.Fatalf("configure %d %s", code, body)
	}
	var state struct {
		Bindings []domain.EdgeBinding `json:"bindings"`
		Pending  int64                `json:"relay_pending"`
	}
	code, body := request("GET", "/config", "", true)
	if code != 200 || json.Unmarshal(body, &state) != nil || len(state.Bindings) != 1 || state.Bindings[0].SourceSessionID != "one" || state.Pending != 0 {
		t.Fatalf("config %d %s", code, body)
	}
	port := strconv.Itoa(freePort(t))
	code, body = request("POST", "/start", `{"port":"`+port+`"}`, true)
	var live service.LiveStatus
	if code != 200 || json.Unmarshal(body, &live) != nil || !live.Edge.Running || !live.AnyRunning {
		t.Fatalf("start %d %s", code, body)
	}
	if code, _ := request("PUT", "/config", `{"bindings":[]}`, true); code < 400 {
		t.Fatal("changed active bindings")
	}
	code, body = request("POST", "/stop", `{}`, true)
	if code != 200 || json.Unmarshal(body, &live) != nil || live.AnyRunning {
		t.Fatalf("stop %d %s", code, body)
	}
	if code, _ := request("PUT", "/config", `{"bindings":[]}`, true); code != 200 {
		t.Fatal("explicit stopped clear failed")
	}
	if code, _ := request("GET", "/journal?after=-1", "", true); code < 400 {
		t.Fatal("invalid export cursor accepted")
	}
	if code, _ := request("GET", "/journal?after=0", "", true); code != 200 {
		t.Fatal("journal unavailable")
	}
}
