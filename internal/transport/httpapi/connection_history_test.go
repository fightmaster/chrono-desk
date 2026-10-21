package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestConnectionHistoryExportRequiresLocalControlCredential(t *testing.T) {
	srv := startTestServer(t)
	request, err := http.NewRequest(http.MethodPost, srv.BaseURL()+"/api/diagnostics/connections/export", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated export=%d", response.StatusCode)
	}
}
