package service

import (
	"context"
	"reflect"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

func progressInt(v int64) *int64   { return &v }
func progressStr(v string) *string { return &v }

func TestRaceProgressSeparatesExpectedFinishersFromOfficialStates(t *testing.T) {
	race := domain.Race{ID: "race", Format: domain.FormatFixedDistance, StartedAtMs: progressInt(1000)}
	members := []domain.Member{
		{ID: "finished", Status: domain.StatusOK, StartTimeMs: progressInt(1000), FinishTimeMs: progressInt(2000), CleanTime: progressStr("00:00:01.000")},
		{ID: "dns", Status: domain.StatusDNS}, {ID: "dnf", Status: domain.StatusDNF}, {ID: "dsq", Status: domain.StatusDSQ},
		{ID: "mass-start", Status: domain.StatusOK, Number: progressInt(12)},
		{ID: "individual", Status: domain.StatusOK, StartTimeMs: progressInt(2000), Number: progressInt(2)},
		{ID: "future", Status: domain.StatusOK, StartTimeMs: progressInt(5000)},
		{ID: "reserve", Status: domain.StatusOK},
		{ID: "incomplete-finish", Status: domain.StatusOK, FinishTimeMs: progressInt(2000)},
	}
	for i := range members {
		members[i].RaceID = "race"
	}
	before := append([]domain.Member(nil), members...)
	ranked := ranking.Protocol(race, members, nil)
	progress := buildRaceProgress(race, members, ranked, nil, map[string]bool{"reserve": true}, 3000)
	want := RaceProgressCounts{Total: 8, Finished: 1, OnCourse: 2, NotStarted: 1, DNS: 1, DNF: 1, DSQ: 1, NeedsReview: 1, Reserve: 1}
	if progress.Counts != want {
		t.Fatalf("counts=%+v want=%+v", progress.Counts, want)
	}
	if len(progress.OnCourse) != 2 || progress.OnCourse[0].MemberID != "individual" || progress.OnCourse[1].MemberID != "mass-start" {
		t.Fatalf("expected roster=%+v", progress.OnCourse)
	}
	if !reflect.DeepEqual(members, before) || !reflect.DeepEqual(ranked, ranking.Protocol(race, members, nil)) {
		t.Fatal("operational view changed timing/ranking input")
	}
}

func TestRaceProgressNeedsAStartedRaceOrIndividualStart(t *testing.T) {
	for _, start := range []*int64{nil, progressInt(0), progressInt(5000)} {
		race := domain.Race{ID: "race", Format: domain.FormatFixedDistance, StartedAtMs: start}
		members := []domain.Member{{ID: "unknown", Status: domain.StatusOK}, {ID: "started", Status: domain.StatusOK, StartTimeMs: progressInt(1000)}}
		progress := buildRaceProgress(race, members, nil, nil, nil, 3000)
		if progress.Counts.OnCourse != 1 || progress.Counts.NotStarted != 1 || progress.OnCourse[0].MemberID != "started" {
			t.Fatalf("unknown/future mass start counted as running: %+v", progress)
		}
	}
}

func TestTimeLimitedProgressDoesNotCallCurrentRankedRunnersFinished(t *testing.T) {
	limit := int64(10)
	race := domain.Race{ID: "race", Format: domain.FormatTimeLimited, StartedAtMs: progressInt(1000), TimeLimitSeconds: &limit}
	members := []domain.Member{{ID: "runner", RaceID: "race", Status: domain.StatusOK, StartTimeMs: progressInt(1000)}}
	place := 1
	ranked := []ranking.Row{{Member: members[0], Place: &place}}
	during := buildRaceProgress(race, members, ranked, nil, nil, 5000)
	if during.Counts.OnCourse != 1 || during.Counts.Finished != 0 {
		t.Fatalf("current lap result mistaken for finish: %+v", during)
	}
	after := buildRaceProgress(race, members, ranked, nil, nil, 11000)
	if after.Counts.OnCourse != 0 || after.Counts.Finished != 1 {
		t.Fatalf("expired race still expected: %+v", after)
	}
	withoutResult := buildRaceProgress(race, members, nil, nil, nil, 11000)
	if withoutResult.Counts.NeedsReview != 1 || withoutResult.Counts.OnCourse != 0 {
		t.Fatal("expired unclassified runner silently made finisher/DNF")
	}
}

func TestProtocolProgressExcludesPacketReservesWithoutChangingOfficialRows(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	ctx := context.Background()
	before, err := BuildProtocol(ctx, store, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO packet_issuance_registrations(event_id,registration_id,value_json,heads_json,deleted)
		VALUES('ev-100','mem-1','{"reserve":true}','[]',0)`); err != nil {
		t.Fatal(err)
	}
	after, err := BuildProtocol(ctx, store, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if after.Progress.Counts.Reserve != 1 || after.Progress.Counts.OnCourse != 0 || after.Progress.Counts.DNS != 1 {
		t.Fatalf("reserve counted as expected: %+v", after.Progress)
	}
	if !reflect.DeepEqual(before.Rows, after.Rows) || before.Counts != after.Counts {
		t.Fatal("operational reserve filtering changed official projection")
	}
}

func TestRaceProgressPartitionsEveryRegistrationWithoutInventingDNS(t *testing.T) {
	race := domain.Race{ID: "race", Format: domain.FormatFixedDistance, StartedAtMs: progressInt(1000)}
	members := []domain.Member{
		{ID: "silent", RaceID: "race"},
		{ID: "dns-with-times", RaceID: "race", Status: domain.StatusDNS, FinishTimeMs: progressInt(2000)},
		{ID: "dnf-with-times", RaceID: "race", Status: domain.StatusDNF, FinishTimeMs: progressInt(2000)},
		{ID: "dsq-with-times", RaceID: "race", Status: domain.StatusDSQ, FinishTimeMs: progressInt(2000)},
		{ID: "unknown-status", RaceID: "race", Status: domain.MemberStatus(99)},
	}
	progress := buildRaceProgress(race, members, nil, nil, nil, 3000)
	c := progress.Counts
	if c.Total != len(members) || c.Total != c.Finished+c.OnCourse+c.NotStarted+c.DNS+c.DNF+c.DSQ+c.NeedsReview {
		t.Fatalf("registration partition lost or duplicated an athlete: %+v", c)
	}
	if c.OnCourse != 1 || c.DNS != 1 || c.DNF != 1 || c.DSQ != 1 || c.NeedsReview != 1 || progress.OnCourse[0].MemberID != "silent" {
		t.Fatalf("silence inferred DNS or official status ignored: %+v", progress)
	}
	empty := buildRaceProgress(race, nil, nil, nil, nil, 3000)
	if empty.OnCourse == nil || empty.Counts.Total != 0 {
		t.Fatal("empty roster must serialize as an array")
	}
}

func TestProtocolProgressManualFinishRemovesExpectedAthlete(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	ctx := context.Background()
	before, err := BuildProtocol(ctx, store, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if before.Progress.Counts.OnCourse != 1 {
		t.Fatalf("initial expectation: %+v", before.Progress)
	}
	if _, err := ManualFinish(ctx, store, "ev-100", "mem-1", 1780813800000); err != nil {
		t.Fatal(err)
	}
	after, err := BuildProtocol(ctx, store, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if after.Progress.Counts.OnCourse != 0 || after.Progress.Counts.Finished != 1 || after.Progress.Counts.DNS != 1 {
		t.Fatalf("manual finisher still expected: %+v", after.Progress)
	}
}

func TestProtocolProgressIgnoresDeletedAndForeignReserveMetadata(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "foreign", Name: "Other event", Date: "2026-06-07", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO packet_issuance_registrations(event_id,registration_id,value_json,heads_json,deleted)
		VALUES('ev-100','mem-1','{"reserve":true}','[]',1)`); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"deleted", "foreign"} {
		if scope == "foreign" {
			// Registration IDs are globally unique: move the synthetic metadata
			// instead of creating an impossible duplicate primary key.
			if _, err := store.DB().Exec(`UPDATE packet_issuance_registrations SET event_id='foreign',deleted=0 WHERE registration_id='mem-1'`); err != nil {
				t.Fatal(err)
			}
		}
		protocol, err := BuildProtocol(ctx, store, "race-10k")
		if err != nil {
			t.Fatal(err)
		}
		if protocol.Progress.Counts.Reserve != 0 || protocol.Progress.Counts.OnCourse != 1 {
			t.Fatalf("%s reserve hid an active participant: %+v", scope, protocol.Progress)
		}
	}
}
