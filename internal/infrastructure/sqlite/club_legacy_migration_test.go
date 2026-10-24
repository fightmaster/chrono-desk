package sqlite

import (
	"context"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestLegacyClubMigrationCapturesOnceAndRespectsExplicitClear(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "Synthetic"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{ID: "race", EventID: "event", Name: "5K", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	team := "Historical club"
	for _, id := range []string{"legacy", "cleared"} {
		if err := store.UpsertMember(ctx, domain.Member{ID: id, EventID: "event", RaceID: "race", Team: &team}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ApplyMemberClubs(ctx, "event", map[string]*string{"cleared": nil}); err != nil {
		t.Fatal(err)
	}
	// Simulate a file made before the one-time migration existed.
	if _, err := store.DB().Exec(`DELETE FROM club_metadata_versions WHERE name='legacy-team-v2'`); err != nil {
		t.Fatal(err)
	}
	before, _ := store.LiveFinishRankRevision(ctx, "event")
	if err := migrateLegacyClubAffiliation(store.DB()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.LiveFinishRankRevision(ctx, "event")
	if before != after {
		t.Fatal("migration changed timing/rank revision")
	}
	clubs, err := store.MemberClubs(ctx, "event")
	if err != nil {
		t.Fatal(err)
	}
	if clubs["legacy"] == nil || *clubs["legacy"] != team {
		t.Fatal("legacy value not copied", clubs)
	}
	if club, known := clubs["cleared"]; !known || club != nil {
		t.Fatal("explicit clear resurrected", clubs)
	}
	if _, err := store.DB().Exec(`UPDATE members SET team='New actual team' WHERE id='legacy'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyClubAffiliation(store.DB()); err != nil {
		t.Fatal(err)
	}
	member, err := store.GetMember(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if member.Club == nil || *member.Club != team || member.Team == nil || *member.Team != "New actual team" {
		t.Fatal("Team edit reinterpreted as Club")
	}
	var original string
	if err := store.DB().QueryRow(`SELECT legacy_team FROM member_club_legacy_values WHERE member_id='legacy'`).Scan(&original); err != nil || original != team {
		t.Fatal("provenance missing", err)
	}
}
