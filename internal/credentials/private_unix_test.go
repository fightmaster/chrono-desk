//go:build !windows

package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelayTLSFailsClosedWithoutPrivateBundle(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(directory, "client-key.pem")
	if err := os.WriteFile(key, []byte("synthetic-private-do-not-log"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivatePath(directory, true); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivatePath(key, false); err != nil {
		t.Fatal(err)
	}
	if _, err := RelayTLS(directory); err == nil || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("malformed key accepted or leaked in diagnostic")
	}
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivatePath(key, false); err == nil {
		t.Fatal("world-readable key accepted")
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivatePath(link, false); err == nil {
		t.Fatal("symlink key accepted")
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivatePath(directory, true); err == nil {
		t.Fatal("shared credential directory accepted")
	}
}
