package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Assembles the run5 sync payload from the event database: chip logs (with
// disabled_at), manual results, journal-derived member/config edits and
// offline-created (local-) members. Deterministic ordering so an unchanged
// event produces byte-identical payloads (idempotent re-push detection).

const syncSchemaVersion = 2

// Fields that round-trip to run5, mirroring the edit whitelist in edits.go.
var (
	syncEventFields      = set("use_race_date_for_age")
	syncMemberFields     = set("status", "number", "epc", "category_id", "first_name", "last_name", "gender", "dob", "team", "city", "start_time_ms", "race_id")
	syncRaceFields       = set("started_at_ms", "name", "category_excludes_top_by_gender")
	syncCheckpointFields = set("since_ms", "since_offset_seconds", "sleep_after_prev_seconds", "board", "sort", "type")
)

type syncEventEdit struct {
	EventID string                     `json:"event_id"`
	Fields  map[string]json.RawMessage `json:"fields"`
}

type syncMemberRef struct {
	MemberID *string `json:"member_id"` // run5 id (nil for local- members)
	LocalID  *string `json:"local_id"`  // chrono-desk local- id (nil for site members)
	Bib      *int64  `json:"bib"`
	EPC      *string `json:"epc"`
}

type syncRfidLog struct {
	ID         string `json:"id"`
	EventID    string `json:"event_id"`
	Status     int    `json:"status"`
	Number     int64  `json:"number"`
	TimeMs     int64  `json:"time_ms"`
	Ant        int    `json:"ant"`
	EPC        string `json:"epc"`
	RSSI       int    `json:"rssi"`
	Board      string `json:"board"`
	DisabledAt *int64 `json:"disabled_at"`
}

type syncRfidLogEdit struct {
	ID         string          `json:"id"`
	DisabledAt json.RawMessage `json:"disabled_at"`
}

type syncNewMember struct {
	LocalID    string  `json:"local_id"`
	RaceID     string  `json:"race_id"`
	FirstName  string  `json:"first_name"`
	LastName   string  `json:"last_name"`
	Number     *int64  `json:"number"`
	EPC        *string `json:"epc"`
	Gender     *string `json:"gender"`
	DOB        *string `json:"dob"`
	CategoryID *string `json:"category_id"`
	Team       *string `json:"team"`
	City       *string `json:"city"`
}

type syncMemberEdit struct {
	MemberRef syncMemberRef              `json:"member_ref"`
	Fields    map[string]json.RawMessage `json:"fields"`
}

type syncManualResult struct {
	MemberRef syncMemberRef `json:"member_ref"`
	RaceID    string        `json:"race_id"`
	TimeMs    int64         `json:"time_ms"`
	Number    *int64        `json:"number"`
}

type syncCheckpointEdit struct {
	CheckpointID string                     `json:"checkpoint_id"`
	Fields       map[string]json.RawMessage `json:"fields"`
}

type syncRaceEdit struct {
	RaceID string                     `json:"race_id"`
	Fields map[string]json.RawMessage `json:"fields"`
}

type syncCheckpointCreate struct {
	ID                    string `json:"id"`
	RaceID                string `json:"race_id"`
	Name                  string `json:"name"`
	Type                  int    `json:"type"`
	Sort                  int64  `json:"sort"`
	Board                 string `json:"board"`
	SinceMs               *int64 `json:"since_ms"`
	SinceOffsetSeconds    *int64 `json:"since_offset_seconds"`
	SleepAfterPrevSeconds *int64 `json:"sleep_after_prev_seconds"`
}

type syncPayload struct {
	SchemaVersion int    `json:"schema_version"`
	EventID       string `json:"event_id"`
	Source        string `json:"source"`
	Overwrite     bool   `json:"overwrite"`

	RfidLogs             []syncRfidLog          `json:"rfid_logs"`
	RfidLogEdits         []syncRfidLogEdit      `json:"rfid_log_edits"`
	NewMembers           []syncNewMember        `json:"new_members"`
	MemberEdits          []syncMemberEdit       `json:"member_edits"`
	ManualResults        []syncManualResult     `json:"manual_results"`
	CheckpointEdits      []syncCheckpointEdit   `json:"checkpoint_edits"`
	EventEdits           []syncEventEdit        `json:"event_edits"`
	RaceEdits            []syncRaceEdit         `json:"race_edits"`
	DeletedManualResults []syncManualResult     `json:"deleted_manual_results"`
	CheckpointCreates    []syncCheckpointCreate `json:"checkpoint_creates"`
	CheckpointDeletes    []string               `json:"checkpoint_deletes"`
	// Per-race category attachments (run5's category_race pivot). Attaches are
	// the desired-state from the live pivot (idempotent syncWithoutDetaching on
	// run5); detaches come from the journal (overwrite-gated detach on run5).
	CategoryAttaches []categoryRacePair `json:"category_attaches"`
	CategoryDetaches []categoryRacePair `json:"category_detaches"`
}

// SyncSummary is the count of what the payload carries (shown in the UI).
type SyncSummary struct {
	RfidLogs          int `json:"rfid_logs"`
	RfidLogEdits      int `json:"rfid_log_edits"`
	NewMembers        int `json:"new_members"`
	MemberEdits       int `json:"member_edits"`
	ManualResults     int `json:"manual_results"`
	CheckpointCreates int `json:"checkpoint_creates"`
	CheckpointEdits   int `json:"checkpoint_edits"`
	EventEdits        int `json:"event_edits"`
	RaceEdits         int `json:"race_edits"`
	CategoryAttaches  int `json:"category_attaches"`
	CategoryDetaches  int `json:"category_detaches"`
}

// BuildSyncPayload assembles the deterministic JSON push payload for an event.
func BuildSyncPayload(ctx context.Context, store *sqlite.Store, eventID string, overwrite bool) ([]byte, SyncSummary, error) {
	p := syncPayload{
		SchemaVersion: syncSchemaVersion, EventID: eventID, Source: "chrono-desk", Overwrite: overwrite,
		RfidLogs: []syncRfidLog{}, RfidLogEdits: []syncRfidLogEdit{}, NewMembers: []syncNewMember{}, MemberEdits: []syncMemberEdit{},
		ManualResults: []syncManualResult{}, CheckpointEdits: []syncCheckpointEdit{}, EventEdits: []syncEventEdit{}, RaceEdits: []syncRaceEdit{},
		DeletedManualResults: []syncManualResult{}, CheckpointCreates: []syncCheckpointCreate{}, CheckpointDeletes: []string{},
		CategoryAttaches: []categoryRacePair{}, CategoryDetaches: []categoryRacePair{},
	}

	// Full member records: used for member_ref resolution AND to emit offline
	// (local-) members as new_members from the live table — their _created
	// journal entry may be absent (e.g. after a backup restore), so the table
	// is the source of truth (same reasoning as offline checkpoints).
	members, err := store.ListMembersFullByEvent(ctx, eventID)
	if err != nil {
		return nil, SyncSummary{}, err
	}
	memberByID := make(map[string]domain.Member, len(members))
	for _, m := range members {
		memberByID[m.ID] = m
	}
	for _, m := range members {
		if !strings.HasPrefix(m.ID, "local-") {
			continue
		}
		p.NewMembers = append(p.NewMembers, syncNewMember{
			LocalID: m.ID, RaceID: m.RaceID, FirstName: m.FirstName, LastName: m.LastName,
			Number: m.Number, EPC: m.EPC, Gender: m.Gender, DOB: m.DOB,
			CategoryID: m.CategoryID, Team: m.Team, City: m.City,
		})
	}
	ref := func(memberID string) syncMemberRef {
		r := syncMemberRef{}
		if strings.HasPrefix(memberID, "local-") {
			id := memberID
			r.LocalID = &id
		} else {
			id := memberID
			r.MemberID = &id
		}
		if m, ok := memberByID[memberID]; ok {
			r.Bib = m.Number
			r.EPC = m.EPC
		}
		return r
	}

	// rfid_logs (ListRfidLogs is already ordered by time_ms, id).
	logs, err := store.ListRfidLogs(ctx, eventID)
	if err != nil {
		return nil, SyncSummary{}, err
	}
	for _, l := range logs {
		p.RfidLogs = append(p.RfidLogs, syncRfidLog{
			ID: l.ID, EventID: l.EventID, Status: l.Status, Number: l.Number, TimeMs: l.TimeMs,
			Ant: l.Ant, EPC: l.EPC, RSSI: l.RSSI, Board: l.Board, DisabledAt: l.DisabledAt,
		})
	}

	// manual results (ordered by time_ms, id).
	manual, err := store.ListManualResults(ctx, eventID, "")
	if err != nil {
		return nil, SyncSummary{}, err
	}
	for _, mr := range manual {
		row := syncManualResult{MemberRef: ref(mr.MemberID), RaceID: mr.RaceID, TimeMs: mr.TimeMs}
		if m, ok := memberByID[mr.MemberID]; ok {
			row.Number = m.Number
		}
		p.ManualResults = append(p.ManualResults, row)
	}

	// Journal: collapse to last value per (entity,id,field); split into
	// new_members (_created on local-) and field edits.
	changes, err := store.ListLocalChanges(ctx)
	if err != nil {
		return nil, SyncSummary{}, err
	}
	memberEdits := map[string]map[string]json.RawMessage{}
	eventEdits := map[string]map[string]json.RawMessage{}
	raceEdits := map[string]map[string]json.RawMessage{}
	cpEdits := map[string]map[string]json.RawMessage{}
	rfidLogEdits := map[string]json.RawMessage{}
	cpDeleted := map[string]bool{}
	catAttached := map[string]categoryRacePair{}
	catDetached := map[string]categoryRacePair{}
	for _, c := range changes {
		switch {
		case c.Entity == "event" && syncEventFields[c.Field]:
			putEdit(eventEdits, c.EntityID, c.Field, c.NewValue)
		case c.Entity == "member" && c.Field == "_manual_finish_deleted":
			var d struct {
				MemberID string `json:"member_id"`
				RaceID   string `json:"race_id"`
				TimeMs   int64  `json:"time_ms"`
			}
			if json.Unmarshal([]byte(c.NewValue), &d) == nil && d.MemberID != "" {
				p.DeletedManualResults = append(p.DeletedManualResults, syncManualResult{
					MemberRef: ref(d.MemberID), RaceID: d.RaceID, TimeMs: d.TimeMs,
				})
			}
		// Field edits only for SITE members; local- ones ship in full as
		// new_members (above), so an edit to them is redundant/unresolvable.
		case c.Entity == "member" && syncMemberFields[c.Field] && !strings.HasPrefix(c.EntityID, "local-"):
			putEdit(memberEdits, c.EntityID, c.Field, c.NewValue)
		case c.Entity == "rfid_log" && c.Field == "disabled_at":
			rfidLogEdits[c.EntityID] = json.RawMessage(c.NewValue)
		case c.Entity == "race" && syncRaceFields[c.Field]:
			putEdit(raceEdits, c.EntityID, c.Field, c.NewValue)
		case c.Entity == "checkpoint" && c.Field == "_deleted":
			cpDeleted[c.EntityID] = true
		case c.Entity == "race_category" && c.Field == "_attached":
			var pr categoryRacePair
			if json.Unmarshal([]byte(c.NewValue), &pr) == nil && pr.RaceID != "" && pr.CategoryID != "" {
				catAttached[pr.RaceID+"\x00"+pr.CategoryID] = pr
			}
		case c.Entity == "race_category" && c.Field == "_detached":
			var pr categoryRacePair
			if json.Unmarshal([]byte(c.NewValue), &pr) == nil && pr.RaceID != "" && pr.CategoryID != "" {
				catDetached[pr.RaceID+"\x00"+pr.CategoryID] = pr
			}
		// Field edits only for SITE checkpoints; local- ones are sent in full as
		// checkpoint_creates (below), so an edit to them would be unresolvable.
		case c.Entity == "checkpoint" && syncCheckpointFields[c.Field] && !strings.HasPrefix(c.EntityID, "local-"):
			putEdit(cpEdits, c.EntityID, c.Field, c.NewValue)
		}
	}

	// checkpoint_creates come from the live table — every offline (local-)
	// checkpoint with its CURRENT config. The journal's _created entry may be
	// absent (e.g. created before journaling or restored from a backup), so the
	// table is the source of truth. run5 mints a numeric id and matches by
	// signature for idempotency.
	checkpoints, err := store.ListCheckpointsFullByEvent(ctx, eventID)
	if err != nil {
		return nil, SyncSummary{}, err
	}
	for _, cp := range checkpoints {
		if !strings.HasPrefix(cp.ID, "local-") {
			continue
		}
		p.CheckpointCreates = append(p.CheckpointCreates, syncCheckpointCreate{
			ID: cp.ID, RaceID: cp.RaceID, Name: cp.Name, Type: int(cp.Type), Sort: cp.Sort, Board: cp.Board,
			SinceMs: cp.SinceMs, SinceOffsetSeconds: cp.SinceOffsetSeconds, SleepAfterPrevSeconds: cp.SleepAfterPrevSeconds,
		})
	}
	// Site checkpoints deleted offline (local- ones never reached the site).
	for _, id := range sortedBoolKeys(cpDeleted) {
		if strings.HasPrefix(id, "local-") {
			continue
		}
		p.CheckpointDeletes = append(p.CheckpointDeletes, id)
	}

	// category_attaches: LOCAL additions only — journaled _attached pairs that
	// are still present in the live pivot. We deliberately do NOT push the whole
	// pivot: the imported baseline already exists on the site, and re-sending it
	// would let a non-overwrite (additive) push resurrect a link the site removed
	// after our export — run5 applies attaches via syncWithoutDetaching, ungated
	// by overwrite, which cuts against "site is source of truth". Only genuine
	// local edits sync back, exactly like member_edits.
	pivot, err := store.ListCategoryRaces(ctx, eventID)
	if err != nil {
		return nil, SyncSummary{}, err
	}
	attached := make(map[string]bool, len(pivot))
	for _, cr := range pivot {
		attached[cr.RaceID+"\x00"+cr.CategoryID] = true
	}
	for _, key := range sortedPairKeys(catAttached) {
		pr := catAttached[key]
		if !attached[pr.RaceID+"\x00"+pr.CategoryID] {
			continue // attached then detached locally — nothing left to add
		}
		p.CategoryAttaches = append(p.CategoryAttaches, pr)
	}
	// category_detaches: journaled detaches whose pair is no longer attached (a
	// re-attach would already ship in category_attaches).
	for _, key := range sortedPairKeys(catDetached) {
		pr := catDetached[key]
		if attached[pr.RaceID+"\x00"+pr.CategoryID] {
			continue
		}
		p.CategoryDetaches = append(p.CategoryDetaches, pr)
	}

	for _, id := range sortedKeys(memberEdits) {
		p.MemberEdits = append(p.MemberEdits, syncMemberEdit{MemberRef: ref(id), Fields: memberEdits[id]})
	}
	for _, id := range sortedKeys(eventEdits) {
		p.EventEdits = append(p.EventEdits, syncEventEdit{EventID: id, Fields: eventEdits[id]})
	}
	for _, id := range sortedKeys(raceEdits) {
		p.RaceEdits = append(p.RaceEdits, syncRaceEdit{RaceID: id, Fields: raceEdits[id]})
	}
	for _, id := range sortedKeys(cpEdits) {
		p.CheckpointEdits = append(p.CheckpointEdits, syncCheckpointEdit{CheckpointID: id, Fields: cpEdits[id]})
	}
	for _, id := range sortedRawKeys(rfidLogEdits) {
		p.RfidLogEdits = append(p.RfidLogEdits, syncRfidLogEdit{ID: id, DisabledAt: rfidLogEdits[id]})
	}

	data, err := json.Marshal(p)
	if err != nil {
		return nil, SyncSummary{}, fmt.Errorf("encode sync payload: %w", err)
	}
	summary := SyncSummary{
		RfidLogs: len(p.RfidLogs), RfidLogEdits: len(p.RfidLogEdits), NewMembers: len(p.NewMembers), MemberEdits: len(p.MemberEdits),
		ManualResults: len(p.ManualResults), CheckpointCreates: len(p.CheckpointCreates),
		CheckpointEdits: len(p.CheckpointEdits), EventEdits: len(p.EventEdits), RaceEdits: len(p.RaceEdits),
		CategoryAttaches: len(p.CategoryAttaches), CategoryDetaches: len(p.CategoryDetaches),
	}
	return data, summary, nil
}

func sortedRawKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func putEdit(m map[string]map[string]json.RawMessage, id, field, rawValue string) {
	if m[id] == nil {
		m[id] = map[string]json.RawMessage{}
	}
	m[id][field] = json.RawMessage(rawValue) // last write wins (changes are oldest→newest)
}

func sortedKeys(m map[string]map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedBoolKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedPairKeys(m map[string]categoryRacePair) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}
