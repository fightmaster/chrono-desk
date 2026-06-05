package service

import (
	"context"
	"encoding/json"
	"fmt"

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
	"race": {
		"started_at_ms": {table: "races", kind: kindInt, recountNeeded: true},
		"name":          {table: "races", kind: kindText},
	},
	"checkpoint": {
		"since_ms":                 {table: "checkpoints", kind: kindInt, recountNeeded: true},
		"since_offset_seconds":     {table: "checkpoints", kind: kindInt, recountNeeded: true},
		"sleep_after_prev_seconds": {table: "checkpoints", kind: kindInt, recountNeeded: true},
		"board":                    {table: "checkpoints", kind: kindText, recountNeeded: true},
		"sort":                     {table: "checkpoints", kind: kindInt, recountNeeded: true},
	},
	"member": {
		"status":        {table: "members", kind: kindInt}, // ranking-only
		"number":        {table: "members", kind: kindInt, recountNeeded: true},
		"epc":           {table: "members", kind: kindText, recountNeeded: true},
		"category_id":   {table: "members", kind: kindText},
		"first_name":    {table: "members", kind: kindText},
		"last_name":     {table: "members", kind: kindText},
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

	old, err := store.UpdateEntityField(ctx, spec.table, req.Field, req.EntityID, value)
	if err != nil {
		return EditResult{}, err
	}

	oldJSON, err := json.Marshal(normalizeDriverValue(old))
	if err != nil {
		return EditResult{}, fmt.Errorf("encode old value: %w", err)
	}
	if err := store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity:   req.Entity,
		EntityID: req.EntityID,
		Field:    req.Field,
		OldValue: string(oldJSON),
		NewValue: string(req.Value),
	}); err != nil {
		return EditResult{}, err
	}

	return EditResult{RecountNeeded: spec.recountNeeded}, nil
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
		if len(c.Field) > 0 && c.Field[0] == '_' {
			continue // pseudo-fields (_created): audit/sync entries, not replays
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
