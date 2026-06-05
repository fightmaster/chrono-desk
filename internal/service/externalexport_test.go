package service

import (
	"context"
	"io"
	"log"
	"os"
	"testing"
)

// Acceptance smoke test for a real run5 export file. Skipped unless
// CHRONO_EXPORT_FILE points at a JSON produced by `php artisan event:export`
// or the admin download button:
//
//	CHRONO_EXPORT_FILE=~/Downloads/event-....json go test -run TestImportExternalExportFile ./internal/service/
//
// It runs the full offline pipeline — parse → import → recount → protocol —
// and fails on any contract violation along the way.
func TestImportExternalExportFile(t *testing.T) {
	path := os.Getenv("CHRONO_EXPORT_FILE")
	if path == "" {
		t.Skip("set CHRONO_EXPORT_FILE to a run5 export JSON to run")
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	store := newTestStore(t)
	ctx := context.Background()

	export, err := ParseEventExport(f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	stats, err := NewEventImporter(store).Import(ctx, export)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	t.Logf("imported: %+v (timezone %s)", stats, export.Timezone)
	if stats.Races == 0 || stats.Members == 0 {
		t.Fatal("export has no races or members")
	}
	if stats.Checkpoints == 0 && stats.RfidLogs > 0 {
		t.Fatal("export has rfid logs but no checkpoints — recount impossible")
	}

	rec := NewRecounter(store, log.New(io.Discard, "", 0), false)
	rstats, err := rec.Recount(ctx, export.Event.ID, "")
	if err != nil {
		t.Fatalf("recount: %v", err)
	}
	t.Logf("recount: %+v", rstats)

	races, err := store.ListRaces(ctx, export.Event.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, race := range races {
		protocol, err := BuildProtocol(ctx, store, race.ID)
		if err != nil {
			t.Fatalf("protocol %s: %v", race.Name, err)
		}
		finished := 0
		for _, row := range protocol.Rows {
			if row.Place != nil {
				finished++
			}
		}
		t.Logf("race %q (%s): rows=%d finished=%d", race.Name, race.Format, len(protocol.Rows), finished)

		if _, _, err := BuildProtocolXLSX(ctx, store, race.ID); err != nil {
			t.Fatalf("xlsx %s: %v", race.Name, err)
		}
	}
}
