package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

var (
	ErrInvalidPacketFeedQuery  = errors.New("invalid packet feed query")
	ErrPacketFeedCursorAhead   = errors.New("packet feed cursor ahead")
	ErrPacketFeedCursorExpired = errors.New("packet feed cursor expired")
	ErrPacketFeedScope         = errors.New("packet feed scope mismatch")
)

func PacketIssuanceBootstrap(ctx context.Context, events *EventService, eventID string, includeOrigins ...bool) (packetissuance.Bootstrap, error) {
	store, err := events.Open(eventID)
	if err != nil {
		return packetissuance.Bootstrap{}, err
	}
	if len(includeOrigins) != 0 && includeOrigins[0] {
		ready, err := store.PacketReserveOriginsReady(ctx, eventID)
		if err != nil {
			return packetissuance.Bootstrap{}, err
		}
		if !ready {
			state, found, err := events.GetPacketRelay(ctx, eventID)
			if err != nil {
				return packetissuance.Bootstrap{}, err
			}
			if found && state.Credential != "" {
				snapshot, fetchErr := FetchPacketBootstrap(ctx, PacketRelayDescriptor{APIBaseURL: state.APIBaseURL, ScopeID: state.ScopeID, EventID: eventID}, state.Credential)
				if fetchErr == nil {
					if err := store.WithinTx(ctx, func(tx *sqlite.Store) error {
						return tx.SavePacketReserveOrigins(ctx, eventID, snapshot.ReserveOrigins)
					}); err != nil {
						return packetissuance.Bootstrap{}, err
					}
				}
			}
		}
	}
	var result packetissuance.Bootstrap
	err = store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		scope, err := txStore.GetPacketIssuanceScope(ctx, eventID)
		if err != nil || scope.ScopeID == "" {
			return errors.New("packet issuance roster is not installed")
		}
		event, err := FirstEvent(ctx, txStore)
		if err != nil {
			return err
		}
		races, err := txStore.ListRaces(ctx, eventID)
		if err != nil {
			return err
		}
		rows, err := txStore.ListPacketRegistrations(ctx, eventID)
		if err != nil {
			return err
		}
		head, err := txStore.PacketFeedHead(ctx, eventID)
		if err != nil {
			return err
		}
		result = packetissuance.Bootstrap{SchemaVersion: 2, ScopeID: scope.ScopeID, SourceKind: "site",
			Event:         packetissuance.Event{ID: event.ID, Name: event.Name, Date: event.Date},
			Registrations: rows, BaselineID: scope.BaselineID, FeedCursor: strconv.FormatInt(head, 10),
		}
		if len(includeOrigins) != 0 && includeOrigins[0] {
			result.ReserveOrigins, err = txStore.PacketReserveOrigins(ctx, eventID, rows)
			if err != nil {
				return err
			}
		}
		for _, race := range races {
			result.Races = append(result.Races, packetissuance.Race{ID: race.ID, Name: race.Name})
		}
		return nil
	})
	return result, err
}

type packetLANFeedAction struct {
	ActionID   string          `json:"actionId"`
	Kind       string          `json:"kind"`
	Sequence   string          `json:"sequence"`
	RecordedAt string          `json:"recordedAt"`
	SourceCode string          `json:"sourceCode"`
	Outcome    string          `json:"outcome"`
	Code       *string         `json:"code"`
	Operation  json.RawMessage `json:"operation"`
	Changes    json.RawMessage `json:"changes"`
}

type packetLANFeedEnvelope struct {
	SchemaVersion int                       `json:"schemaVersion"`
	ScopeID       string                    `json:"scopeId"`
	Cursor        packetissuance.FeedCursor `json:"cursor"`
	Actions       []packetLANFeedAction     `json:"actions"`
}

func PacketIssuanceFeedPage(ctx context.Context, store *sqlite.Store, eventID, scopeID, after string, limit int) (packetissuance.FeedPage, error) {
	cursor, err := strconv.ParseInt(after, 10, 64)
	if err != nil || cursor < 0 || strconv.FormatInt(cursor, 10) != after || limit < 1 || limit > 100 {
		return packetissuance.FeedPage{}, ErrInvalidPacketFeedQuery
	}
	var page packetissuance.FeedPage
	err = store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		scope, err := txStore.GetPacketIssuanceScope(ctx, eventID)
		if err != nil || scope.ScopeID != scopeID {
			return ErrPacketFeedScope
		}
		bounds, err := txStore.PacketFeedBounds(ctx, eventID)
		if err != nil {
			return err
		}
		if cursor > bounds.Head {
			return ErrPacketFeedCursorAhead
		}
		if cursor < bounds.FirstAvailable-1 {
			return ErrPacketFeedCursorExpired
		}
		rows, err := txStore.ListPacketFeedRows(ctx, eventID, cursor, limit)
		if err != nil {
			return err
		}
		next := cursor
		actions := make([]packetLANFeedAction, 0, len(rows))
		for _, row := range rows {
			if row.Sequence != next+1 {
				return errors.New("packet feed sequence gap")
			}
			next = row.Sequence
			operation := json.RawMessage("null")
			if len(row.OperationJSON) != 0 {
				operation = append(json.RawMessage(nil), row.OperationJSON...)
			}
			actions = append(actions, packetLANFeedAction{ActionID: row.ActionID, Kind: row.Kind,
				Sequence: strconv.FormatInt(row.Sequence, 10), RecordedAt: time.UnixMilli(row.RecordedAt).UTC().Format("2006-01-02T15:04:05.000Z"),
				SourceCode: row.SourceCode, Outcome: row.Outcome, Code: row.OutcomeCode,
				Operation: operation, Changes: append(json.RawMessage(nil), row.ChangesJSON...),
			})
		}
		envelope := packetLANFeedEnvelope{SchemaVersion: 1, ScopeID: scopeID,
			Cursor: packetissuance.FeedCursor{After: after, Next: strconv.FormatInt(next, 10),
				Head: strconv.FormatInt(bounds.Head, 10), HasMore: next < bounds.Head}, Actions: actions,
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		page, err = packetissuance.ParseFeedPage(raw, scopeID, after)
		return err
	})
	return page, err
}
