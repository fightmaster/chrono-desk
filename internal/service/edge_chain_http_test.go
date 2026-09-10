//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Real HTTP/SQL acceptance complements Laravel's in-process tests, whose
// normal testing environment bypasses CSRF. No test login route is installed.
func TestEdgeChainCentralHTTPSourceAdministration(t *testing.T) {
	runEdgeChainReceivers(t, "plate", func(t *testing.T, hub *edgeChainHub, source *edgeChainSource, store *sqlite.Store) {
		c := newEdgeChainCentral(t, hub, "plate-test", "plate-one")
		c.waitRows(t, 3)
		c.snapshot(t, "http-setup")
		c.startHTTP(t)
		const host = "app.chrono.localhost"
		const path = "/event/100/rfid/sources"
		payload := func(token string) url.Values {
			return url.Values{"_token": {token}, "board": {"plate-test"}, "source_session_id": {"http-session"}, "reason": {"<script>synthetic-audit</script>"}}
		}
		unchanged := func(before edgeChainCentralSnapshot) {
			after := c.snapshot(t, "snapshot")
			if !reflect.DeepEqual(before.SourceBindings, after.SourceBindings) || !reflect.DeepEqual(before.SourceActions, after.SourceActions) || !reflect.DeepEqual(before.Rows, after.Rows) {
				t.Fatal("rejected HTTP request changed source bindings, audit or raw observations")
			}
		}
		before := c.snapshot(t, "snapshot")
		guest := newEdgeHTTPClient(t, hub.id)
		token := edgeHTTPToken(t, guest.request(t, host, "GET", "/login", nil, 200))
		guest.request(t, host, "POST", path, payload(token), 401)
		unchanged(before)
		denied := newEdgeHTTPClient(t, hub.id)
		denied.login(t, host, "denied@example.invalid")
		// A session cookie and valid CSRF do not grant the events permission.
		token = edgeHTTPToken(t, denied.request(t, host, "GET", "/user/profile", nil, 200))
		denied.request(t, host, "POST", path, payload(token), 403)
		unchanged(before)

		admin := newEdgeHTTPClient(t, hub.id)
		stale := admin.login(t, host, "fixture@example.invalid")
		page := admin.request(t, host, "GET", "/event/100/rfid", nil, 200)
		token = edgeHTTPToken(t, page)
		if strings.Contains(page, "private-other-session") || strings.Contains(page, "private-other-reason") {
			t.Fatal("another event's source binding or audit leaked into the admin page")
		}
		for _, invalid := range []string{"", "invalid-token", stale} {
			admin.request(t, host, "POST", path, payload(invalid), 419)
			unchanged(before)
		}
		invalidBoard := payload(token)
		invalidBoard.Set("board", "Plate-test")
		admin.request(t, host, "POST", path, invalidBoard, 422)
		unchanged(before)
		admin.request(t, host, "POST", path, payload(token), 302)
		after := c.snapshot(t, "snapshot")
		if len(after.SourceBindings) != len(before.SourceBindings)+1 || len(after.SourceActions) != len(before.SourceActions)+1 {
			t.Fatal("HTTP grant did not append exactly one binding and one audit")
		}
		last := after.SourceActions[len(after.SourceActions)-1]
		if fmt.Sprint(last["actor_id"]) != "1" || last["action"] != "grant" || last["reason"] != "<script>synthetic-audit</script>" {
			t.Fatalf("HTTP audit lost the authenticated actor or reason: %+v", last)
		}
		page = admin.request(t, host, "GET", "/event/100/rfid", nil, 200)
		if !strings.Contains(page, "http-session") || !strings.Contains(page, "&lt;script&gt;synthetic-audit&lt;/script&gt;") || strings.Contains(page, "<script>synthetic-audit</script>") {
			t.Fatal("HTTP admin page omitted the grant or failed to escape its audit reason")
		}
		token = edgeHTTPToken(t, page)
		admin.request(t, host, "POST", path, payload(token), 302)
		unchanged(after) // Retrying a successful form must not replace its audit.
		id := fmt.Sprint(after.SourceBindings[0]["id"])
		change := url.Values{"_token": {token}, "enabled": {"0"}, "reason": {"HTTP revoke"}}
		admin.request(t, host, "POST", "/event/200/rfid/sources/"+id, change, 404)
		unchanged(after)
		admin.request(t, "chrono.localhost", "POST", path, payload(token), 404)
		unchanged(after)
		otherSite := newEdgeHTTPClient(t, hub.id)
		otherSite.login(t, "app.run5.localhost", "fixture@example.invalid")
		otherToken := edgeHTTPToken(t, otherSite.request(t, "app.run5.localhost", "GET", "/user/profile", nil, 200))
		otherSite.request(t, "app.run5.localhost", "POST", path, payload(otherToken), 403)
		unchanged(after)

		admin.request(t, host, "POST", path+"/"+id, change, 302)
		number := int64(4)
		epc := "E2000004"
		if err := store.UpsertMember(t.Context(), domain.Member{ID: "4", EventID: "100", RaceID: "race", Number: &number, EPC: &epc}); err != nil {
			t.Fatal(err)
		}
		source.read(t, 4)
		source.waitACKs(t, 4, 4)
		c.waitPendingRetry(t)
		if len(c.snapshot(t, "snapshot").Rows) != 3 {
			t.Fatal("central accepted a source revoked through HTTP")
		}
		publications := len(hub.entries(t))
		change.Set("enabled", "1")
		change.Set("reason", "HTTP resume")
		admin.request(t, host, "POST", path+"/"+id, change, 302)
		c.waitRows(t, 4)
		if len(hub.entries(t)) != publications {
			t.Fatal("HTTP resume required source retransmission")
		}
		final := c.snapshot(t, "snapshot")
		if len(final.SourceActions) != len(after.SourceActions)+2 {
			t.Fatal("HTTP revoke/resume did not append exactly two audits")
		}
		for i, action := range []string{"revoke", "enable"} {
			audit := final.SourceActions[len(after.SourceActions)+i]
			if audit["action"] != action || fmt.Sprint(audit["actor_id"]) != "1" || audit["reason"] != []string{"HTTP revoke", "HTTP resume"}[i] {
				t.Fatalf("HTTP change audit: %+v", audit)
			}
		}
		guest.request(t, host, "GET", "/event/100/export", nil, 302)
		final.Export = json.RawMessage(admin.request(t, host, "GET", "/event/100/export", nil, 200))
		c.assertMetadata(t, store, final, 4)

		// Generate a real event token through the existing admin form, then
		// use the actual API without an authenticated browser session.
		admin.request(t, host, "POST", "/event/100/sync-token/generate", url.Values{"_token": {token}}, 302)
		page = admin.request(t, host, "GET", "/event/100/rfid", nil, 200)
		found := regexp.MustCompile(`class="font-mono break-all">([A-Za-z0-9]{64})</span>`).FindStringSubmatch(page)
		if len(found) != 2 {
			t.Fatal("admin token form did not render its one-time generated token")
		}
		api := newEdgeHTTPClient(t, hub.id)
		api.request(t, host, "GET", "/api/sync/events/100/changes", nil, 401)
		api.syncToken = "invalid-synthetic-token"
		api.request(t, host, "GET", "/api/sync/events/100/changes", nil, 403)
		api.syncToken = found[1]
		api.request(t, host, "GET", "/api/sync/events/200/changes", nil, 403)
		feed := api.request(t, host, "GET", "/api/sync/events/100/changes?limit=100", nil, 200)
		decoder := json.NewDecoder(strings.NewReader(feed))
		decoder.UseNumber()
		if err := decoder.Decode(&final.Feed); err != nil {
			t.Fatal(err)
		}
		final.Export = json.RawMessage(api.request(t, host, "GET", "/api/sync/events/100", nil, 200))
		c.assertMetadata(t, store, final, 4)
		t.Log("real HTTP login/cookies/CSRF, scoped permissions/audit, revoke/resume, token authorization and feed/export metadata passed")
	})
}

func (c *edgeChainCentral) startHTTP(t *testing.T) {
	t.Helper()
	build := filepath.Join(os.Getenv("EDGE_RUN5_ROOT"), "public", "build")
	if _, err := os.Stat(filepath.Join(build, "manifest.json")); err != nil {
		t.Fatal("HTTP fixture requires the real RUN5 production frontend build")
	}
	c.hub.docker(t, "cp", build, c.phpID+":/fixture/public/build")
	c.hub.docker(t, "exec", "-d", "--env", "SESSION_DRIVER=file", "--env", "SESSION_SECURE_COOKIE=false", c.phpID,
		"/bin/sh", "-c", "exec php -S 127.0.0.1:8098 -t /fixture/public /fixture/public/index.php > /tmp/edge-http.log 2>&1")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := edgeChainDocker("exec", c.phpID, "tail", "-60", "/tmp/edge-http.log")
			t.Logf("PHP HTTP fixture: %s", out)
		}
	})
	edgeChainWait(t, "actual PHP HTTP listener", func() bool {
		_, err := edgeChainDocker("exec", c.hub.id, "/bin/busybox", "nc", "-z", "-w", "1", "127.0.0.1", "8098")
		return err == nil
	})
}

type edgeHTTPClient struct {
	client    *http.Client
	syncToken string
}

func newEdgeHTTPClient(t *testing.T, hubID string) *edgeHTTPClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &edgeHTTPClient{client: &http.Client{Jar: jar, Transport: edgeHTTPTransport{hubID}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// One finite HTTP request closes tunnel stdin before waiting for the response.
// The long-lived RFID byte tunnel cannot delimit close-framed HTTP responses.
// Laravel, not this bridge, owns HTTP status, headers, cookies and response body.
type edgeHTTPTransport struct{ hubID string }

func (transport edgeHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var wire bytes.Buffer
	wireRequest := request.Clone(request.Context())
	wireRequest.Close = true
	if err := wireRequest.Write(&wire); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(request.Context(), "docker", "exec", "-i", transport.hubID, "/bin/busybox", "nc", "127.0.0.1", "8098")
	cmd.Stdin = &wire
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("HTTP fixture transport: %w", err)
	}
	return http.ReadResponse(bufio.NewReader(bytes.NewReader(output)), request)
}

func (c *edgeHTTPClient) request(t *testing.T, host, method, path string, values url.Values, want int) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+host+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
	}
	if c.syncToken != "" {
		req.Header.Set("X-SYNC-TOKEN", c.syncToken)
	}
	response, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		if len(body) > 1500 {
			body = body[:1500]
		}
		t.Fatalf("HTTP %s %s%s: status=%d want=%d location=%s body=%s", method, host, path, response.StatusCode, want, response.Header.Get("Location"), body)
	}
	return string(body)
}

func (c *edgeHTTPClient) login(t *testing.T, host, email string) string {
	t.Helper()
	token := edgeHTTPToken(t, c.request(t, host, "GET", "/login", nil, 200))
	c.request(t, host, "POST", "/login", url.Values{"email": {email}, "password": {"synthetic-password"}, "_token": {token}}, 302)
	return token
}

func edgeHTTPToken(t *testing.T, body string) string {
	t.Helper()
	for _, pattern := range []string{`name="_token" value="([^"]+)"`, `name="csrf-token" content="([^"]+)"`} {
		if found := regexp.MustCompile(pattern).FindStringSubmatch(body); len(found) == 2 {
			return html.UnescapeString(found[1])
		}
	}
	t.Fatal("actual HTML did not contain a CSRF token")
	return ""
}
