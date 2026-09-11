//go:build linux && edgeintegration && edgecentralintegration && edgebrowser

package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEdgeManagementWebsiteBrowser(t *testing.T) {
	node := edgeChainBinary(t, "EDGE_NODE_BINARY")
	browser := edgeChainBinary(t, "EDGE_BROWSER_BINARY")
	hub := newEdgeChainHub(t, "plate-test", "plate-one")
	c := newEdgeChainCentral(t, hub, "plate-test", "plate-one")
	c.snapshot(t, "http-setup")
	c.snapshot(t, "management-setup")
	server := c.managementHTTPS(t, nil)
	artifacts := t.TempDir()
	if root := os.Getenv("EDGE_BROWSER_ARTIFACTS"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("EDGE_BROWSER_ARTIFACTS must be an absolute existing directory")
		}
		var err error
		artifacts, err = os.MkdirTemp(root, "management-browser-")
		if err != nil {
			t.Fatal(err)
		}
		t.Log("retained synthetic browser screenshots:", artifacts)
	}
	spki := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	base := strings.Replace(server.URL, "127.0.0.1", "app.chrono.localhost", 1)
	script, err := filepath.Abs("testdata/management-browser-smoke.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script, base, base64.StdEncoding.EncodeToString(spki[:]), artifacts)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TZ=UTC", "EDGE_BROWSER_BINARY=" + browser}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("management browser: %v\n%s", err, out)
	}
	t.Log(string(out))
	st := c.managementSnapshot(t)
	if len(st.Devices) != 1 || st.Devices[0].RevokedAt == nil || len(st.Commands) != 1 || st.Commands[0].State != "cancelled" || st.RawCount != 0 {
		t.Fatal("browser registration/revocation did not match expected isolated state")
	}
}
