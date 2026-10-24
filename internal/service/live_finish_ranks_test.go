package service

import (
	"context"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func TestLiveFinishRanksCacheTracksOfficialInputsAndIgnoresRawTraffic(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	ctx := context.Background()
	exec := func(query string) {
		t.Helper()
		if _, err := store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE races SET category_excludes_top_by_gender=0 WHERE id='race-10k'`)
	exec(`UPDATE members SET status=0,gender='male',category_id='cat-m18',start_time_ms=1000,finish_time_ms=101000,clean_time='00:01:40' WHERE id='mem-1'`)
	exec(`UPDATE members SET status=0,gender='male',category_id='cat-m18',start_time_ms=1000,finish_time_ms=111000,clean_time='00:01:50' WHERE id='mem-2'`)
	cache := liveFinishRankCache{}
	load := func() map[string]liveFinishPlaces {
		t.Helper()
		race, err := store.GetRace(ctx, "race-10k")
		if err != nil {
			t.Fatal(err)
		}
		revision, err := store.LiveFinishRankRevision(ctx, "ev-100")
		if err != nil {
			t.Fatal(err)
		}
		places, err := cache.places(ctx, store, race, revision)
		if err != nil {
			t.Fatal(err)
		}
		return places
	}
	assertPlace := func(id string, gender, category int) {
		t.Helper()
		row := load()[id]
		if row.Gender == nil || *row.Gender != gender || row.Category == nil || *row.Category != category {
			t.Fatalf("unexpected places for %s: %+v", id, row)
		}
	}
	assertPlace("mem-2", 2, 2)
	assertPlace("mem-1", 1, 1)
	if cache.builds != 1 {
		t.Fatal("unchanged snapshot reranked", cache.builds)
	}
	exec(`INSERT INTO rfid_logs(id,event_id,time_ms,board,epc) VALUES('unrelated-raw','ev-100',999999,'B','other')`)
	assertPlace("mem-2", 2, 2)
	if cache.builds != 1 {
		t.Fatal("raw traffic invalidated ranks", cache.builds)
	}
	exec(`UPDATE members SET finish_time_ms=91000 WHERE id='mem-2'`)
	assertPlace("mem-2", 1, 1)
	if cache.builds != 2 {
		t.Fatal("finish correction did not invalidate ranks", cache.builds)
	}
	exec(`UPDATE members SET clean_time=NULL WHERE id='mem-2'`)
	assertPlace("mem-1", 1, 1)
	if load()["mem-2"].Gender != nil || cache.builds != 3 {
		t.Fatal("clean eligibility did not invalidate ranks")
	}
	exec(`UPDATE members SET clean_time='00:01:30' WHERE id='mem-2'`)
	assertPlace("mem-2", 1, 1)
	exec(`UPDATE members SET status=2 WHERE id='mem-2'`)
	assertPlace("mem-1", 1, 1)
	if load()["mem-2"].Gender != nil {
		t.Fatal("DNF kept an official place")
	}
	exec(`UPDATE members SET status=0 WHERE id='mem-2'`)
	exec(`UPDATE races SET category_excludes_top_by_gender=1 WHERE id='race-10k'`)
	if row := load()["mem-2"]; row.Gender == nil || *row.Gender != 1 || row.Category != nil {
		t.Fatal("top-by-gender exclusion ignored")
	}
	builds := cache.builds
	exec(`UPDATE members SET team='Updated club',city='Updated city' WHERE id='mem-1'`)
	load()
	if cache.builds != builds {
		t.Fatal("display-only metadata reranked the race")
	}
}

func TestOlderLiveFinishSnapshotDoesNotReplaceNewerCachedRanks(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	ctx := context.Background()
	cache := liveFinishRankCache{}
	race, err := store.GetRace(ctx, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	newer := sqlite.LiveFinishRankRevision{Config: 20, Eligibility: 2}
	older := sqlite.LiveFinishRankRevision{Config: 19, Eligibility: 1}
	if _, err = cache.places(ctx, store, race, newer); err != nil {
		t.Fatal(err)
	}
	if _, err = cache.places(ctx, store, race, older); err != nil {
		t.Fatal(err)
	}
	if _, err = cache.places(ctx, store, race, newer); err != nil {
		t.Fatal(err)
	}
	if cache.builds != 2 {
		t.Fatal("older snapshot evicted newer cache", cache.builds)
	}
}
