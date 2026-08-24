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
