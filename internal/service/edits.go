package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Local offline edits ("старт задержали"): whitelisted fields are updated in
// place, journaled into local_changes, and — per the chosen conflict policy —
// re-applied on top of every event re-import (local edits win; the journal is
// the audit trail and the future to-site sync list).

type fieldKind int

const (
	kindInt fieldKind = iota // unix-millis or plain integers; null allowed
	kindText
)

type editableField struct {
	table string
	kind  fieldKind
	// recountNeeded marks fields that change derivation (times, matching),
	// not just display/ranking.
	recountNeeded bool
}

var editWhitelist = map[string]map[string]editableField{
	"event": {
		"use_race_date_for_age": {table: "events", kind: kindInt},
	},
	"race": {
		"started_at_ms": {table: "races", kind: kindInt, recountNeeded: true},
		"name":          {table: "races", kind: kindText},
		// 0/1: exclude the overall top-3 per gender from category standings
		// (run5's ExcludeTopByGender strategy). Ranking-only — no recount.
		"category_excludes_top_by_gender": {table: "races", kind: kindInt},
	},
	"checkpoint": {
		"since_ms":                 {table: "checkpoints", kind: kindInt, recountNeeded: true},
		"since_offset_seconds":     {table: "checkpoints", kind: kindInt, recountNeeded: true},
		"sleep_after_prev_seconds": {table: "checkpoints", kind: kindInt, recountNeeded: true},
		"board":                    {table: "checkpoints", kind: kindText, recountNeeded: true},
		"sort":                     {table: "checkpoints", kind: kindInt, recountNeeded: true},
		// 1=START/2=CHECKPOINT/3=FINISH. Editable so a mistyped checkpoint can be
		// fixed without delete+recreate; changes derivation, so recount.
		"type": {table: "checkpoints", kind: kindInt, recountNeeded: true},
	},
	"member": {
		"status":        {table: "members", kind: kindInt}, // ranking-only
		"number":        {table: "members", kind: kindInt, recountNeeded: true},
		"epc":           {table: "members", kind: kindText, recountNeeded: true},
		"category_id":   {table: "members", kind: kindText},
		"first_name":    {table: "members", kind: kindText},
		"last_name":     {table: "members", kind: kindText},
		"gender":        {table: "members", kind: kindText}, // "male" | "female" | null
		"dob":           {table: "members", kind: kindText}, // ISO date; display-only
		"team":          {table: "members", kind: kindText},
		"city":          {table: "members", kind: kindText},
		"start_time_ms": {table: "members", kind: kindInt, recountNeeded: true},
		"race_id":       {table: "members", kind: kindText, recountNeeded: true},
	},
	"rfid_log": {
		"disabled_at": {table: "rfid_logs", kind: kindInt, recountNeeded: true},
	},
}

// EditRequest is the API payload for one field change.
type EditRequest struct {
	Entity   string          `json:"entity"`
	EntityID string          `json:"entity_id"`
	Field    string          `json:"field"`
	Value    json.RawMessage `json:"value"` // null | number | string
}

// EditResult tells the UI whether a recount is required to see the effect.
type EditResult struct {
	RecountNeeded bool `json:"recount_needed"`
}

// ApplyEdit validates against the whitelist, updates the field and journals
// the change.
func ApplyEdit(ctx context.Context, store *sqlite.Store, req EditRequest) (EditResult, error) {
	spec, value, err := validateEdit(req)
	if err != nil {
		return EditResult{}, err
	}
	if err := store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		return applyValidatedEdit(ctx, txStore, req, spec, value)
	}); err != nil {
		return EditResult{}, err
	}
	return EditResult{RecountNeeded: spec.recountNeeded}, nil
}

func applyValidatedEdit(ctx context.Context, store *sqlite.Store, req EditRequest, spec editableField, value any) error {
	old, err := store.UpdateEntityField(ctx, spec.table, req.Field, req.EntityID, value)
	if err != nil {
		return err
	}

	oldJSON, err := json.Marshal(normalizeDriverValue(old))
	if err != nil {
		return fmt.Errorf("encode old value: %w", err)
	}
	if err := store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity:   req.Entity,
		EntityID: req.EntityID,
		Field:    req.Field,
		OldValue: string(oldJSON),
		NewValue: string(req.Value),
	}); err != nil {
		return err
	}

	if err := maybeRecalculateMemberCategory(ctx, store, req); err != nil {
		return err
	}

	// Delayed/advanced start: when the race start moves, the whole field moves
	// with it by the same delta — so chip-less mass starters follow, and a
	// staggered start keeps its 30s gaps. We need the old start to size the
	// delta; UpdateEntityField returned it above. Each shift is journaled as a
	// member edit so it survives re-import and syncs back to the site.
	if req.Entity == "race" && req.Field == "started_at_ms" {
		if newStart, ok := value.(int64); ok {
			if oldStart, ok := asInt64(old); ok {
				if err := shiftMemberStarts(ctx, store, req.EntityID, newStart-oldStart); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func maybeRecalculateMemberCategory(ctx context.Context, store *sqlite.Store, req EditRequest) error {
	if req.Entity != "member" {
		return nil
	}
	if req.Field == "category_id" && string(req.Value) != "null" {
		return nil
	}
	switch req.Field {
	case "dob", "gender", "race_id", "category_id":
	default:
		return nil
	}

	member, err := store.GetMember(ctx, req.EntityID)
	if err != nil {
		return err
	}
	oldCategoryID := member.CategoryID
	newCategoryID, err := resolveCategoryIDForMember(ctx, store, member)
	if err != nil {
		return err
	}
	if stringPtrEqual(oldCategoryID, newCategoryID) {
		return nil
	}

	oldJSON, err := json.Marshal(oldCategoryID)
	if err != nil {
		return fmt.Errorf("encode old category_id: %w", err)
	}
	newJSON, err := json.Marshal(newCategoryID)
	if err != nil {
		return fmt.Errorf("encode new category_id: %w", err)
	}

	if _, err := store.UpdateEntityField(ctx, "members", "category_id", req.EntityID, newCategoryID); err != nil {
		return err
	}
	return store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity:   "member",
		EntityID: req.EntityID,
		Field:    "category_id",
		OldValue: string(oldJSON),
		NewValue: string(newJSON),
	})
}

// asInt64 coerces a driver scan result (int64, or a numeric []byte/string) to
// int64; reports false for NULL or non-numeric values.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case []byte:
		i, err := strconv.ParseInt(string(n), 10, 64)
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

// shiftMemberStarts moves every member's start by deltaMs and journals each
// change (entity "member", field "start_time_ms").
func shiftMemberStarts(ctx context.Context, store *sqlite.Store, raceID string, deltaMs int64) error {
	shifts, err := store.ShiftMemberStarts(ctx, raceID, deltaMs)
	if err != nil {
		return err
	}
	for _, sh := range shifts {
		oldJSON, err := json.Marshal(sh.OldStartMs)
		if err != nil {
			return fmt.Errorf("encode old start: %w", err)
		}
		newJSON, err := json.Marshal(sh.NewStartMs)
		if err != nil {
			return fmt.Errorf("encode new start: %w", err)
		}
		if err := store.InsertLocalChange(ctx, sqlite.LocalChange{
			Entity: "member", EntityID: sh.MemberID, Field: "start_time_ms",
			OldValue: string(oldJSON), NewValue: string(newJSON),
		}); err != nil {
			return err
		}
	}
	return nil
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ReapplyLocalEdits replays the journal on top of freshly imported site data
// (local edits win). Entries whose entity vanished from the export are
// skipped silently.
func ReapplyLocalEdits(ctx context.Context, store *sqlite.Store) (applied int, err error) {
	changes, err := store.ListLocalChanges(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range changes {
		if c.Field == "_deleted" && c.Entity == "checkpoint" {
			// Re-imports resurrect site checkpoints; re-delete what the judge
			// removed (idempotent — no-op when already absent).
			if err := store.DeleteCheckpointCascade(ctx, c.EntityID); err == nil {
				applied++
			}
			continue
		}
		if c.Entity == "race_category" && (c.Field == "_attached" || c.Field == "_detached") {
			// The import replaced the event's pivot with the site's; replay the
			// judge's attach/detach on top (local wins). Processed oldest→newest,
			// so the last action for a pair wins. Both are idempotent.
			var p categoryRacePair
			if json.Unmarshal([]byte(c.NewValue), &p) != nil || p.RaceID == "" || p.CategoryID == "" {
				continue
			}
			var e error
			if c.Field == "_attached" {
				e = store.AttachRaceCategory(ctx, p.RaceID, p.CategoryID)
			} else {
				e = store.DetachRaceCategory(ctx, p.RaceID, p.CategoryID)
			}
			if e == nil {
				applied++
			}
			continue
		}
		if len(c.Field) > 0 && c.Field[0] == '_' {
			continue // other pseudo-fields (_created): audit/sync entries only
		}
		spec, value, err := validateEdit(EditRequest{
			Entity: c.Entity, EntityID: c.EntityID, Field: c.Field, Value: json.RawMessage(c.NewValue),
		})
		if err != nil {
			continue // journal predates a whitelist change — skip
		}
		if _, err := store.UpdateEntityField(ctx, spec.table, c.Field, c.EntityID, value); err != nil {
			continue // entity no longer exists in the export
		}
		applied++
	}
	return applied, nil
}

func validateEdit(req EditRequest) (editableField, any, error) {
	fields, ok := editWhitelist[req.Entity]
	if !ok {
		return editableField{}, nil, fmt.Errorf("entity %q is not editable", req.Entity)
	}
	spec, ok := fields[req.Field]
	if !ok {
		return editableField{}, nil, fmt.Errorf("field %s.%s is not editable", req.Entity, req.Field)
	}
	if req.EntityID == "" {
		return editableField{}, nil, fmt.Errorf("entity_id is required")
	}

	var raw any
	if len(req.Value) > 0 {
		if err := json.Unmarshal(req.Value, &raw); err != nil {
			return editableField{}, nil, fmt.Errorf("invalid value: %w", err)
		}
	}

	switch spec.kind {
	case kindInt:
		switch v := raw.(type) {
		case nil:
			return spec, nil, nil
		case float64:
			return spec, int64(v), nil
		default:
			return editableField{}, nil, fmt.Errorf("field %s.%s expects a number or null", req.Entity, req.Field)
		}
	default: // kindText
		switch v := raw.(type) {
		case nil:
			return spec, nil, nil
		case string:
			return spec, v, nil
		default:
			return editableField{}, nil, fmt.Errorf("field %s.%s expects a string or null", req.Entity, req.Field)
		}
	}
}

// normalizeDriverValue converts driver scan results into JSON-friendly types.
func normalizeDriverValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}
