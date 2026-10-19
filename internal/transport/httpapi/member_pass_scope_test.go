package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func TestMemberPassScopesPreserveHistoryAndManualFacts(t *testing.T) {
	srv := startTestServer(t)
	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	var imported service.ImportStats
	decodeBody(t, mustPost(t, srv.BaseURL()+"/api/events/import", "application/json", strings.NewReader(string(fixture))), &imported)
	store, err := srv.events.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`
		INSERT INTO rfid_logs (id,event_id,epc,board,time_ms,disabled_at) VALUES
		('unused','ev-100','E280AAA','Feibot:U659',1780812700000,NULL),
		('disabled-hit','ev-100','E280AAA','Feibot:U659',1780812800000,1780900000000),
		('other-member','ev-100','E280AAA','Feibot:U659',1780812900000,NULL);
		INSERT INTO results (event_id,race_id,member_id,checkpoint_id,rfid_log_id,time_ms) VALUES
		('ev-100','race-10k','mem-1','cp-finish','log-active',1780812600000),
		('ev-100','race-10k','mem-1','cp-finish','disabled-hit',1780812800000),
		('ev-100','race-10k','mem-2','cp-finish','other-member',1780812900000),
		('ev-100','race-10k','mem-1',NULL,NULL,1780813000000)`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := store.ListRfidLogs(ctx, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	memberBefore, err := store.GetMember(ctx, "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	base := srv.BaseURL() + "/api/events/ev-100/members/mem-1/passes"
	var accepted, legacy, all service.MemberPasses
	decodeBody(t, mustGet(t, base+"?scope=accepted"), &accepted)
	decodeBody(t, mustGet(t, base), &legacy)
	decodeBody(t, mustGet(t, base+"?scope=all"), &all)
	if len(accepted.Passes) != 1 || accepted.Passes[0].LogID != "log-active" || accepted.Passes[0].DisabledAt != nil {
		t.Fatalf("accepted view leaked unaccepted/disabled/other-member history: %+v", accepted.Passes)
	}
	if len(all.Passes) != 4 || !reflect.DeepEqual(legacy, all) {
		t.Fatalf("default all-history compatibility lost: legacy=%+v all=%+v", legacy, all)
	}
	if len(accepted.ManualResults) != 1 || !reflect.DeepEqual(accepted.ManualResults, all.ManualResults) {
		t.Fatalf("manual judge facts changed between scopes: %+v / %+v", accepted.ManualResults, all.ManualResults)
	}
	resp := apiRequest(t, http.MethodGet, base+"?scope=typo", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown scope status=%d", resp.StatusCode)
	}
	after, err := store.ListRfidLogs(ctx, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	memberAfter, err := store.GetMember(ctx, "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) || !reflect.DeepEqual(memberBefore, memberAfter) {
		t.Fatal("read scopes changed raw/disabled/member facts")
	}
}
