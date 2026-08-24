package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFrontendReleaseDependenciesMeetSecurityBaseline(t *testing.T) {
	var packageJSON struct {
		DevDependencies map[string]string `json:"devDependencies"`
		Engines         map[string]string `json:"engines"`
	}
	if err := json.Unmarshal([]byte(readRepositoryFile(t, "frontend/package.json")), &packageJSON); err != nil {
		t.Fatalf("decode frontend/package.json: %v", err)
	}

	want := map[string]string{
		"@sveltejs/vite-plugin-svelte": "^7.3.0",
		"svelte":                       "^5.56.10",
		"vite":                         "^8.2.2",
	}
	for dependency, minimum := range want {
		if got := packageJSON.DevDependencies[dependency]; got != minimum {
			t.Errorf("%s = %q, want audited baseline %q", dependency, got, minimum)
		}
	}
	if got := packageJSON.Engines["node"]; got != "^20.19.0 || >=22.12.0" {
		t.Errorf("Node engine = %q, want Vite 8 runtime floor", got)
	}
	if makefile := readRepositoryFile(t, "Makefile"); !strings.Contains(makefile, "npm audit --audit-level=high") {
		t.Error("clean frontend quality gate does not enforce the audited dependency baseline")
	}
}

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
	if !strings.Contains(goMod, "gitlab.com/fightmaster1/rfid-core v0.2.0") {
		t.Fatal("go.mod does not pin canonical GitLab rfid-core v0.2.0")
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
		"TIMING_MODULES_READ_TOKEN",
		"go mod download gitlab.com/fightmaster1/timing-core gitlab.com/fightmaster1/rfid-core",
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
