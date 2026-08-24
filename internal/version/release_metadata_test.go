package version

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseMetadataIsVersionedChecksummedAndSigned(t *testing.T) {
	version := strings.TrimSpace(readRepositoryFile(t, "VERSION"))
	if version != "0.4.0" {
		t.Fatalf("VERSION = %q, want 0.4.0", version)
	}

	makefile := readRepositoryFile(t, "Makefile")
	for _, required := range []string{
		"git rev-parse HEAD",
		"git show -s --format=%cI HEAD",
	} {
		if !strings.Contains(makefile, required) {
			t.Fatalf("Makefile missing %q", required)
		}
	}
	if strings.Contains(makefile, "git rev-parse --short HEAD") {
		t.Fatal("Makefile stamps an ambiguous short source revision")
	}

	goMod := readRepositoryFile(t, "go.mod")
	if !strings.Contains(goMod, "github.com/fightmaster/rfid-core v0.1.1") {
		t.Fatal("go.mod does not pin accepted immutable rfid-core v0.1.1")
	}
	if strings.Contains(goMod, "replace gitlab.com/fightmaster1/rfid-core") ||
		strings.Contains(goMod, "../rfid-core") {
		t.Fatal("release dependency graph still uses a mutable sibling rfid-core checkout")
	}

	workflow := readRepositoryFile(t, ".github/workflows/build.yml")
	for _, required := range []string{
		"tags: ['v*']",
		"codesign --verify --deep --strict",
		"SHA256SUMS",
		"gh release create",
		"git rev-parse HEAD",
		"git show -s --format=%cI HEAD",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("release workflow missing %q", required)
		}
	}
	if strings.Contains(workflow, "repository: fightmaster/rfid-core") {
		t.Fatal("release workflow still checks out mutable sibling rfid-core")
	}
}

func readRepositoryFile(t *testing.T, name string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}
