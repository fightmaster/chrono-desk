package service

import (
	"context"
	"encoding/json"
	"fmt"
)

type PresenterMetadataStats struct {
	Provided int `json:"provided"`
	Updated  int `json:"updated"`
}

// RefreshPresenterClubs imports display-only metadata without replay, cursors,
// registration changes or writes to any timing/observation table.
func (s *EventService) RefreshPresenterClubs(ctx context.Context, eventID string) (PresenterMetadataStats, error) {
	var stats PresenterMetadataStats
	store, err := s.Open(eventID)
	if err != nil {
		return stats, err
	}
	cfg, err := store.GetSyncConfig(ctx, eventID)
	if err != nil {
		return stats, err
	}
	if cfg.BaseURL == "" || cfg.Token == "" {
		return stats, fmt.Errorf("настройте адрес сайта и токен синхронизации")
	}
	body, err := PullExport(ctx, cfg.BaseURL, cfg.Token, eventID)
	if err != nil {
		return stats, err
	}
	var document struct {
		SchemaVersion int `json:"schema_version"`
		Event         struct {
			ID string `json:"id"`
		} `json:"event"`
		Members []struct {
			ID           string          `json:"id"`
			Club         json.RawMessage `json:"club"`
			ClubPersonID json.RawMessage `json:"club_person_id"`
		} `json:"members"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return stats, fmt.Errorf("данные клуба не разобраны: %w", err)
	}
	if document.SchemaVersion < 1 || document.SchemaVersion > 5 || document.Event.ID != eventID || len(document.Members) > 20000 {
		return stats, fmt.Errorf("неподдерживаемые данные клуба или другое событие")
	}
	clubs := make(map[string]*string)
	owners := map[string]*string{}
	seen := make(map[string]bool)
	for _, member := range document.Members {
		if member.ID == "" || seen[member.ID] {
			return stats, fmt.Errorf("повторная или пустая регистрация в данных клуба")
		}
		seen[member.ID] = true
		if len(member.Club) == 0 {
			continue
		}
		club, err := decodeClubMetadata(member.Club)
		if err != nil {
			return stats, err
		}
		if len(member.ClubPersonID) > 0 {
			var owner *string
			if err := json.Unmarshal(member.ClubPersonID, &owner); err != nil {
				return stats, fmt.Errorf("неверный владелец клуба")
			}
			owners[member.ID] = owner
		}
		clubs[member.ID] = club
	}
	stats.Provided = len(clubs)
	stats.Updated, err = store.ApplyBoundMemberClubs(ctx, eventID, clubs, owners)
	return stats, err
}
