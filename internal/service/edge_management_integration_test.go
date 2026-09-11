//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type managementFixtureDevice struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Snapshot  map[string]any `json:"snapshot"`
	LastSeen  *string        `json:"last_seen_at"`
	RevokedAt *string        `json:"revoked_at"`
}
type managementFixtureSnapshot struct {
	Devices  []managementFixtureDevice `json:"devices"`
	Commands []struct {
		ID       string  `json:"id"`
		DeviceID string  `json:"device_id"`
		Action   string  `json:"action"`
		State    string  `json:"state"`
		Result   *string `json:"result"`
	} `json:"commands"`
	Actions []struct {
		DeviceID string `json:"device_id"`
		Action   string `json:"action"`
	} `json:"actions"`
	RawCount     int    `json:"raw_count"`
	SourceCount  int    `json:"source_count"`
	MySQLVersion string `json:"mysql_version"`
	PHPVersion   string `json:"php_version"`
}

func (c *edgeChainCentral) managementSnapshot(t *testing.T) managementFixtureSnapshot {
	t.Helper()
	output := c.hub.docker(t, "exec", c.phpID, "php", "tests/Support/edge-chain.php", "management-snapshot")
	var st managementFixtureSnapshot
	if err := json.Unmarshal([]byte(output), &st); err != nil {
		t.Fatal("management fixture snapshot:", err)
	}
	return st
}

// Real TLS terminates here; the hidden PHP development server receives the same
// HTTPS=on server metadata normally supplied by the trusted FPM TLS terminator.
// It has no host port or external network. No application auth/CSRF is bypassed.
func (c *edgeChainCentral) managementHTTPS(t *testing.T, loss *managementResponseLoss) *httptest.Server {
	t.Helper()
	c.hub.docker(t, "cp", filepath.Join(os.Getenv("EDGE_RUN5_ROOT"), "public/build"), c.phpID+":/fixture/public/build")
	c.hub.docker(t, "cp", filepath.Join(os.Getenv("EDGE_RUN5_ROOT"), "tests/Support/edge-management-router.php"), c.phpID+":/fixture/tests/Support/edge-management-router.php")
	c.hub.docker(t, "exec", "-d", "--env", "EDGE_MANAGEMENT_ENABLED=true", "--env", "SESSION_DRIVER=file", "--env", "SESSION_SECURE_COOKIE=true", "--env", "PHP_CLI_SERVER_WORKERS=4", c.phpID,
		"/bin/sh", "-c", "exec php -S 127.0.0.1:8098 -t /fixture/public /fixture/tests/Support/edge-management-router.php > /tmp/edge-management-http.log 2>&1")
	edgeChainWait(t, "management PHP listener", func() bool {
		_, err := edgeChainDocker("exec", c.hub.id, "/bin/busybox", "nc", "-z", "-w", "1", "127.0.0.1", "8098")
		return err == nil
	})
	proxy := &httputil.ReverseProxy{Transport: edgeHTTPTransport{c.hub.id}, Director: func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = "http", "app.chrono.localhost"
		if host, _, err := net.SplitHostPort(r.Host); err == nil && net.ParseIP(host) != nil {
			r.Host = "app.chrono.localhost"
		}
		r.Header.Del("Forwarded")
		r.Header.Del("X-Forwarded-Host")
		r.Header.Del("X-Forwarded-Proto")
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "fixture upstream unavailable", http.StatusBadGateway)
	}}
	if loss != nil {
		proxy.ModifyResponse = loss.filter
	}
	server := httptest.NewTLSServer(proxy)
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := edgeChainDocker("exec", c.phpID, "tail", "-40", "/tmp/edge-management-http.log")
			t.Log(out)
		}
	})
	return server
}

// Drop only completed heartbeat replies, after the actual backend transaction.
// No commands or ACKs are fabricated; the client sees an ordinary gateway failure.
type managementResponseLoss struct {
	command atomic.Bool
	result  atomic.Pointer[string]
	retried atomic.Bool
}

func (loss *managementResponseLoss) filter(response *http.Response) error {
	if response.StatusCode != http.StatusOK || !strings.HasSuffix(response.Request.URL.Path, "/heartbeat") {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32769))
	response.Body.Close()
	if err != nil || len(body) > 32768 {
		return errors.New("invalid fixture heartbeat response")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	var reply struct {
		Command      json.RawMessage `json:"command"`
		Acknowledged []string        `json:"acknowledged_results"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return errors.New("invalid fixture heartbeat JSON")
	}
	if len(reply.Command) > 0 && string(reply.Command) != "null" && loss.command.CompareAndSwap(false, true) {
		return errors.New("synthetic lost command response")
	}
	if len(reply.Acknowledged) > 0 {
		id := reply.Acknowledged[0]
		if loss.result.CompareAndSwap(nil, &id) {
			return errors.New("synthetic lost result acknowledgement")
		}
		for _, acknowledged := range reply.Acknowledged {
			if acknowledged == *loss.result.Load() {
				loss.retried.Store(true)
			}
		}
	}
	return nil
}

type managementBrowser struct{ client *http.Client }

func newManagementBrowser(t *testing.T, server *httptest.Server) *managementBrowser {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &managementBrowser{client: &http.Client{Transport: transport, Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *managementBrowser) request(t *testing.T, method, path string, form url.Values, want int) string {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, "https://app.chrono.localhost"+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "application/json")
	}
	response, err := b.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("management HTTP %s %s status=%d want=%d", method, path, response.StatusCode, want)
	}
	return string(body)
}

func (b *managementBrowser) login(t *testing.T, email string) {
	t.Helper()
	token := edgeHTTPToken(t, b.request(t, "GET", "/login", nil, 200))
	b.request(t, "POST", "/login", url.Values{"_token": {token}, "email": {email}, "password": {"synthetic-password"}}, 302)
}

func managementField(t *testing.T, page, name string) string {
	t.Helper()
	match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(page)
	if len(match) != 2 {
		t.Fatal("missing management form field", name)
	}
	return html.UnescapeString(match[1])
}

func registerManagement(t *testing.T, b *managementBrowser, name string) (string, string) {
	t.Helper()
	token := edgeHTTPToken(t, b.request(t, "GET", "/edge-devices", nil, 200))
	page := b.request(t, "POST", "/edge-devices", url.Values{"_token": {token}, "name": {name}}, 200)
	id := regexp.MustCompile(`<code>([a-f0-9-]{36})</code>`).FindStringSubmatch(page)
	key := regexp.MustCompile(`<code>([a-f0-9]{64})</code>`).FindStringSubmatch(page)
	if len(id) != 2 || len(key) != 2 {
		t.Fatal("registration did not render separate ID and key")
	}
	if strings.Contains(b.request(t, "GET", "/edge-devices/"+id[1], nil, 200), key[1]) {
		t.Fatal("key displayed again")
	}
	return id[1], key[1]
}

func enrollManagement(t *testing.T, s *edgeChainSource, endpoint, id, key string) {
	t.Helper()
	path := "/"
	if s.profile == "feibot" {
		path = "/management"
	}
	page, code, err := s.request("GET", path, nil)
	if err != nil || code != 200 {
		t.Fatal("local management page", code, err)
	}
	form := url.Values{"action": {"connect"}, "management_revision": {managementField(t, string(page), "management_revision")}, "endpoint": {endpoint}, "device_id": {id}, "credential": {key}}
	if _, code, err := s.request("POST", "/management", form); err != nil || code != 403 {
		t.Fatal("missing local CSRF accepted", code, err)
	}
	form.Set("csrf_token", managementField(t, string(page), "csrf_token"))
	if _, code, err := s.request("POST", "/management", form); err != nil || code != 303 {
		t.Fatal("local enrollment failed", code, err)
	}
}

func (c *edgeChainCentral) waitManagement(t *testing.T, label string, check func(managementFixtureSnapshot) bool) {
	t.Helper()
	// Deliberately lost replies require extra real 30-second exchanges. The
	// production command expiry and client cadence are unchanged.
	edgeChainWaitWithin(t, label, 150*time.Second, func() bool {
		if check(c.managementSnapshot(t)) {
			return true
		}
		time.Sleep(450 * time.Millisecond)
		return false
	})
}

func TestEdgeManagementActualHTTPSMySQLAndSidecar(t *testing.T) {
	hub := newEdgeChainHub(t, "plate-test", "plate-one")
	c := newEdgeChainCentral(t, hub, "plate-test", "plate-one")
	c.snapshot(t, "http-setup")
	c.snapshot(t, "management-setup")
	loss := &managementResponseLoss{}
	server := c.managementHTTPS(t, loss)
	admin := newManagementBrowser(t, server)
	admin.login(t, "fixture@example.invalid")
	denied := newManagementBrowser(t, server)
	denied.login(t, "denied@example.invalid")
	denied.request(t, "GET", "/edge-devices", nil, 403)
	admin.request(t, "POST", "/edge-devices", url.Values{"name": {"CSRF must fail"}}, 419)
	plateID, plateKey := registerManagement(t, admin, "Management plate")
	feibotID, feibotKey := registerManagement(t, admin, "Management Feibot")
	t.Log("actual HTTPS login, role denial, CSRF and one-time registration passed")
	ca := filepath.Join(t.TempDir(), "management-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	plate := newEdgeChainSource(t, "plate", hub.endpoint, hub.endpoint)
	feibot := newEdgeChainSource(t, "feibot", hub.endpoint, hub.endpoint)
	for _, s := range []*edgeChainSource{plate, feibot} {
		s.extraEnv = []string{"SSL_CERT_FILE=" + ca, "SSL_CERT_DIR=" + filepath.Dir(ca)}
		s.start(t)
	}
	plate.confirmClock(t)
	enrollManagement(t, plate, server.URL, plateID, plateKey)
	enrollManagement(t, feibot, server.URL, feibotID, feibotKey)
	c.waitManagement(t, "both actual sidecar profiles heartbeat", func(st managementFixtureSnapshot) bool {
		if len(st.Devices) != 2 {
			return false
		}
		for _, d := range st.Devices {
			if d.LastSeen == nil || d.Snapshot == nil {
				return false
			}
		}
		return true
	})
	initial := c.managementSnapshot(t)
	for _, d := range initial.Devices {
		status := d.Snapshot["status"].(map[string]any)
		if status["host_time"] == nil || status["queues"] == nil || status["errors"] == nil || status["reader_time"] != nil {
			t.Fatal("telemetry shape mismatch")
		}
		if d.ID == feibotID && (d.Snapshot["revision"] != nil || d.Snapshot["paused"] != nil || len(d.Snapshot["capabilities"].([]any)) != 0) {
			t.Fatal("Feibot advertised unsupported control")
		}
	}
	t.Logf("both profiles reported real telemetry; MySQL=%s PHP=%s", initial.MySQLVersion, initial.PHPVersion)
	command := func(action string, payload url.Values) {
		page := admin.request(t, "GET", "/edge-devices/"+plateID, nil, 200)
		form := url.Values{"_token": {edgeHTTPToken(t, page)}, "action": {action}}
		for _, field := range []string{"expected_boot_id", "expected_event_id", "expected_revision"} {
			form.Set(field, managementField(t, page, field))
		}
		for key, values := range payload {
			form[key] = values
		}
		admin.request(t, "POST", "/edge-devices/"+plateID+"/commands", form, 302)
		c.waitManagement(t, "remote "+action+" applied and result persisted", func(st managementFixtureSnapshot) bool {
			for _, cmd := range st.Commands {
				if cmd.DeviceID == plateID && cmd.Action == action && cmd.State == "applied" && cmd.Result != nil {
					return true
				}
			}
			return false
		})
		t.Log("actual sidecar command/result completed:", action)
	}
	command("pause_input", nil)
	payload := url.Values{}
	for i, id := range []string{"hub", "chrono"} {
		prefix := "payload[destinations][" + []string{"0", "1"}[i] + "]"
		payload.Set(prefix+"[id]", id)
		payload.Set(prefix+"[endpoint]", "tcp://"+hub.endpoint)
		payload.Set(prefix+"[enabled]", "0")
		payload.Set(prefix+"[timeout_ms]", "2000")
	}
	command("configure_destinations", payload)
	command("configure_event", url.Values{"payload[event_id]": {"200"}, "payload[session_id]": {"management-session-200"}, "payload[timezone]": {"UTC"}})
	command("resume_input", nil)
	final := c.managementSnapshot(t)
	if !loss.command.Load() || loss.result.Load() == nil || !loss.retried.Load() {
		t.Fatal("lost command/result replies were not recovered")
	}
	for _, before := range initial.Devices {
		for _, after := range final.Devices {
			if before.ID == plateID && after.ID == plateID && after.Snapshot["revision"].(float64) != before.Snapshot["revision"].(float64)+4 {
				t.Fatal("four remote commands did not produce exactly four local revisions")
			}
		}
	}
	if final.RawCount != initial.RawCount || final.SourceCount != initial.SourceCount || len(final.Commands) != 4 {
		t.Fatal("management changed observations/admission or command count")
	}
	for _, action := range []string{"enqueue", "deliver", "applied"} {
		n := 0
		for _, a := range final.Actions {
			if a.DeviceID == plateID && a.Action == action {
				n++
			}
		}
		if n != 4 {
			t.Fatalf("audit %s=%d want4", action, n)
		}
	}
	page := admin.request(t, "GET", "/edge-devices/"+plateID, nil, 200)
	admin.request(t, "POST", "/edge-devices/"+plateID+"/revoke", url.Values{"_token": {edgeHTTPToken(t, page)}}, 302)
	// A revoked website key does not pause capture or erase local source state.
	plate.read(t, 1)
	edgeChainWait(t, "autonomous capture after revocation", func() bool {
		data, code, err := plate.request("GET", "/api/status", nil)
		if err != nil || code != 200 {
			return false
		}
		var local map[string]any
		if json.Unmarshal(data, &local) != nil {
			return false
		}
		return local["paused"] == false && local["journal"].(map[string]any)["captured"] == float64(1)
	})
	data, code, err := plate.request("GET", "/api/status", nil)
	if err != nil || code != 200 {
		t.Fatal(code, err)
	}
	var local map[string]any
	if err := json.Unmarshal(data, &local); err != nil {
		t.Fatal(err)
	}
	if local["paused"] != false || local["journal"].(map[string]any)["captured"] != float64(1) {
		t.Fatal("revocation stopped autonomous capture")
	}
	before := c.managementSnapshot(t)
	var seen *string
	for _, d := range before.Devices {
		if d.ID == plateID {
			seen = d.LastSeen
		}
	}
	edgeChainWaitWithin(t, "actual client observes revoked credential", 45*time.Second, func() bool {
		data, code, err := plate.request("GET", "/api/status", nil)
		return err == nil && code == 200 && strings.Contains(string(data), "management HTTP status 401")
	})
	after := c.managementSnapshot(t)
	for _, d := range after.Devices {
		if d.ID == plateID && (d.RevokedAt == nil || seen == nil || d.LastSeen == nil || *seen != *d.LastSeen) {
			t.Fatal("revoked heartbeat updated snapshot")
		}
	}
	plate.stop(t)
	plate.start(t)
	data, code, err = plate.request("GET", "/api/status", nil)
	if err != nil || code != 200 || !strings.Contains(string(data), "management-session-200") {
		t.Fatal("source binding lost after restart", code, err)
	}
	t.Log("revocation rejected actual heartbeat, preserved autonomous raw capture and source binding across restart; no timing rows or admission grants changed")
}
