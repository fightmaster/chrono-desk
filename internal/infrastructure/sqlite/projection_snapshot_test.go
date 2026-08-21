package sqlite

import (
	"context"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestProjectionEvidenceTracksResultConfigurationAndInputs(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	if err := store.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "ev1", Name: "Race", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	number := int64(7)
	if err := store.UpsertMember(ctx, domain.Member{ID: "m1", EventID: "ev1", RaceID: "r1", Number: &number}); err != nil {
		t.Fatal(err)
	}

	initial, err := store.ProjectionEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.ProjectionEvidence(ctx, "ev1")
	if err != nil || repeated != initial {
		t.Fatalf("unstable evidence: initial=%+v repeated=%+v err=%v", initial, repeated, err)
	}

	if _, err := store.DB().Exec(`INSERT INTO categories (id, name, min, max, gender) VALUES ('cat1', 'Masters', 40, 49, 'male')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE members SET category_id='cat1', dob='1980-01-01', gender='male' WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	configured, err := store.ProjectionEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if configured.ConfigVersion == initial.ConfigVersion || configured.InputWatermark != initial.InputWatermark {
		t.Fatalf("configuration evidence drift: initial=%+v configured=%+v", initial, configured)
	}

	if err := store.ApplyObservationFeedPage(ctx, "ev1", []domain.RfidLog{{
		ID: "log-1", EventID: "ev1", Number: 7, TimeMs: 1000, Board: "split",
	}}, "cursor-1", 1000); err != nil {
		t.Fatal(err)
	}
	withInput, err := store.ProjectionEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if withInput.ConfigVersion != configured.ConfigVersion || withInput.InputWatermark == configured.InputWatermark {
		t.Fatalf("input evidence drift: configured=%+v input=%+v", configured, withInput)
	}
}

func TestResolveObservationMemberRejectsAmbiguousNumber(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	for _, raceID := range []string{"r1", "r2"} {
		if err := store.UpsertRace(ctx, domain.Race{ID: raceID, EventID: "ev1", Name: raceID, Format: domain.FormatFixedDistance}); err != nil {
			t.Fatal(err)
		}
		number := int64(42)
		if err := store.UpsertMember(ctx, domain.Member{ID: "m-" + raceID, EventID: "ev1", RaceID: raceID, Number: &number}); err != nil {
			t.Fatal(err)
		}
	}
	match, err := store.ResolveObservationMember(ctx, "ev1", 42, "")
	if err != nil {
		t.Fatal(err)
	}
	if !match.Ambiguous || match.Found {
		t.Fatalf("match=%+v, want ambiguous", match)
	}
}
