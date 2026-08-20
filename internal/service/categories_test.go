package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// A v2 export carries the FULL catalog plus an explicit category_races pivot;
// the importer must use the pivot verbatim (NOT seed from members) and keep the
// unattached catalog category.
func TestImportV2FullCatalogAndExplicitPivot(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	doc := `{"schema_version":2,"timezone":"Europe/Moscow","event":{"id":"ev2","name":"E2"},
	  "races":[{"id":"r1","event_id":"ev2","name":"10k","format":"FixedDistance"}],
	  "categories":[
	    {"id":"c1","name":"M","min":18,"max":39,"gender":"male"},
	    {"id":"c2","name":"W","min":18,"max":39,"gender":"female"}
	  ],
	  "category_races":[{"race_id":"r1","category_id":"c1"}]}`
	export, err := ParseEventExport(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := NewEventImporter(store).Import(ctx, export); err != nil {
		t.Fatalf("import: %v", err)
	}

	cat, err := store.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat) != 2 {
		t.Fatalf("catalog = %d, want 2 (full catalog incl. unattached)", len(cat))
	}
	got := raceCatSet(mustListRaceCats(t, store, "r1"))
	if len(got) != 1 || !got["c1"] {
		t.Fatalf("pivot = %v, want only {c1} (explicit, not member-seeded)", got)
	}
}

func raceCatSet(cats []domain.Category) map[string]bool {
	m := map[string]bool{}
	for _, c := range cats {
		m[c.ID] = true
	}
	return m
}

func mustListRaceCats(t *testing.T, store *sqlite.Store, raceID string) []domain.Category {
	t.Helper()
	cats, err := store.ListRaceCategories(context.Background(), raceID)
	if err != nil {
		t.Fatalf("list race categories: %v", err)
	}
	return cats
}

func mustAttach(t *testing.T, store *sqlite.Store, raceID, categoryID string) {
	t.Helper()
	if _, err := AttachCategory(context.Background(), store, "ev-100", raceID, categoryID); err != nil {
		t.Fatalf("attach %s: %v", categoryID, err)
	}
}

func addCatalogCategory(t *testing.T, store *sqlite.Store, id string) {
	t.Helper()
	min, max, g := 1, 99, "female"
	if id == "cat-x" {
		min, max = 100, 120
	}
	if err := store.UpsertCategory(context.Background(), domain.Category{ID: id, Name: id, Min: &min, Max: &max, Gender: &g}); err != nil {
		t.Fatalf("upsert catalog category %s: %v", id, err)
	}
}

// A v1 export carries no category_races pivot, so the importer seeds it from
// member.category_id — the per-race set must not regress to empty.
func TestImportSeedsPivotFromMembersOnV1(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store) // v1 fixture: mem-1 → cat-m18, mem-2 → null

	got := raceCatSet(mustListRaceCats(t, store, "race-10k"))
	if len(got) != 1 || !got["cat-m18"] {
		t.Fatalf("seeded pivot = %v, want {cat-m18}", got)
	}
}

func TestAttachDetachCategoryGuardedAndJournaled(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	addCatalogCategory(t, store, "cat-w18")

	mustAttach(t, store, "race-10k", "cat-w18")
	if !raceCatSet(mustListRaceCats(t, store, "race-10k"))["cat-w18"] {
		t.Fatal("attach did not add cat-w18")
	}

	// Attaching a category that isn't in the catalog must fail.
	if _, err := AttachCategory(ctx, store, "ev-100", "race-10k", "cat-missing"); err == nil {
		t.Fatal("attach of a non-catalog category should fail")
	}

	// Detach is refused while a member of the race is assigned to it (cat-m18
	// has mem-1) — don't strand participants.
	if _, err := DetachCategory(ctx, store, "ev-100", "race-10k", "cat-m18"); err == nil {
		t.Fatal("detach of a category with assigned members should fail")
	}
	// cat-w18 has no members → detach succeeds.
	if _, err := DetachCategory(ctx, store, "ev-100", "race-10k", "cat-w18"); err != nil {
		t.Fatalf("detach unused: %v", err)
	}
	if raceCatSet(mustListRaceCats(t, store, "race-10k"))["cat-w18"] {
		t.Fatal("detach did not remove cat-w18")
	}
}

func TestAttachCategoryRejectsSameGenderAgeOverlap(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	male := "male"
	min, max := 30, 50
	if err := store.UpsertCategory(ctx, domain.Category{
		ID: "cat-overlap", Name: "Overlap", Gender: &male, Min: &min, Max: &max,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := AttachCategory(ctx, store, "ev-100", "race-10k", "cat-overlap"); err == nil {
		t.Fatal("overlapping category attach should fail")
	}
	if raceCatSet(mustListRaceCats(t, store, "race-10k"))["cat-overlap"] {
		t.Fatal("overlapping category was attached")
	}
}

// A re-import replaces the pivot with the site's (seeded from members here); the
// journal's attach must replay on top (local edits win).
func TestReapplyReplaysCategoryAttach(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	addCatalogCategory(t, store, "cat-w18")
	mustAttach(t, store, "race-10k", "cat-w18")

	importFixture(t, store) // replaces pivot with members-seed {cat-m18}, then replays

	got := raceCatSet(mustListRaceCats(t, store, "race-10k"))
	if !got["cat-m18"] {
		t.Fatalf("re-import dropped the site-seeded cat-m18: %v", got)
	}
	if !got["cat-w18"] {
		t.Fatalf("re-import + replay lost the local attach cat-w18: %v", got)
	}
}

// category_attaches carry only LOCAL attaches (journaled, still live), never the
// imported baseline — re-pushing the baseline could resurrect a site-removed link
// on a non-overwrite push. category_detaches come from the journal (only when the
// pair is no longer attached).
func TestSyncPayloadCategoryAttachesAndDetaches(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	addCatalogCategory(t, store, "cat-w18")
	addCatalogCategory(t, store, "cat-x")

	mustAttach(t, store, "race-10k", "cat-w18")
	mustAttach(t, store, "race-10k", "cat-x")
	if _, err := DetachCategory(ctx, store, "ev-100", "race-10k", "cat-x"); err != nil {
		t.Fatal(err)
	}

	data, summary, err := BuildSyncPayload(ctx, store, "ev-100", true)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CategoryAttaches != 1 || summary.CategoryDetaches != 1 {
		t.Fatalf("summary attaches=%d detaches=%d, want 1/1", summary.CategoryAttaches, summary.CategoryDetaches)
	}
	var p syncPayload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	attached := map[string]bool{}
	for _, a := range p.CategoryAttaches {
		attached[a.CategoryID] = true
	}
	// Only the local attach (cat-w18) syncs. cat-m18 is the member-seeded baseline
	// the site already has — re-pushing it could resurrect a site-removed link, so
	// it must NOT appear. cat-x was attached then detached → it belongs in
	// detaches, not attaches.
	if !attached["cat-w18"] || attached["cat-m18"] || attached["cat-x"] {
		t.Fatalf("category_attaches = %+v, want only {cat-w18}", p.CategoryAttaches)
	}
	if len(p.CategoryDetaches) != 1 || p.CategoryDetaches[0].CategoryID != "cat-x" {
		t.Fatalf("category_detaches = %+v, want [cat-x]", p.CategoryDetaches)
	}
}
