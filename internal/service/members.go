package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// On-site registration (package C). Locally created members get a "local-"
// id prefix: they never collide with site ids, survive re-imports untouched
// (exports simply don't carry them) and are recognizable by the future
// to-site sync. Creation is journaled with the pseudo-field "_created" —
// replay skips underscore fields, the entry exists for audit/sync.

// CreateMemberRequest is the registration form payload.
type CreateMemberRequest struct {
	RaceID     string  `json:"race_id"`
	FirstName  string  `json:"first_name"`
	LastName   string  `json:"last_name"`
	Number     *int64  `json:"number"`
	EPC        *string `json:"epc"`
	Gender     *string `json:"gender"`
	CategoryID *string `json:"category_id"`
	DOB        *string `json:"dob"` // ISO date (YYYY-MM-DD), required (run5 parity)
	Team       *string `json:"team"`
	City       *string `json:"city"`
}

// CreateMember validates uniqueness of bib/EPC within the event, inserts the
// member and journals the creation. recount is needed when the new member has
// an EPC — already-imported logs of that tag may now match.
func CreateMember(ctx context.Context, store *sqlite.Store, eventID string, req CreateMemberRequest) (memberID string, result EditResult, err error) {
	if req.LastName == "" && req.FirstName == "" {
		return "", EditResult{}, fmt.Errorf("укажите имя участника")
	}
	// run5 parity: a participant cannot exist without a birth date.
	if req.DOB == nil || *req.DOB == "" {
		return "", EditResult{}, fmt.Errorf("укажите дату рождения")
	}
	if _, err := time.Parse("2006-01-02", *req.DOB); err != nil {
		return "", EditResult{}, fmt.Errorf("дата рождения должна быть в формате ГГГГ-ММ-ДД")
	}
	race, err := store.GetRace(ctx, req.RaceID)
	if err != nil || race.EventID != eventID {
		return "", EditResult{}, fmt.Errorf("гонка %s не найдена в событии", req.RaceID)
	}

	if req.Number != nil {
		taken, err := fieldTaken(ctx, store, eventID, "number", *req.Number)
		if err != nil {
			return "", EditResult{}, err
		}
		if taken {
			return "", EditResult{}, fmt.Errorf("номер %d уже занят в этом событии", *req.Number)
		}
	}
	if req.EPC != nil && *req.EPC != "" {
		taken, err := fieldTaken(ctx, store, eventID, "epc", *req.EPC)
		if err != nil {
			return "", EditResult{}, err
		}
		if taken {
			return "", EditResult{}, fmt.Errorf("метка %s уже занята в этом событии", *req.EPC)
		}
	}

	memberID = "local-" + randomHex(8)
	member := domain.Member{
		ID: memberID, EventID: eventID, RaceID: req.RaceID,
		FirstName: req.FirstName, LastName: req.LastName,
		Number: req.Number, EPC: req.EPC, Gender: req.Gender,
		CategoryID: req.CategoryID, DOB: req.DOB, Team: req.Team, City: req.City,
	}
	if member.CategoryID == nil {
		member.CategoryID, err = resolveCategoryIDForMember(ctx, store, member)
		if err != nil {
			return "", EditResult{}, err
		}
	}
	if err := store.UpsertMember(ctx, member); err != nil {
		return "", EditResult{}, err
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return "", EditResult{}, fmt.Errorf("encode member: %w", err)
	}
	if err := store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity:   "member",
		EntityID: memberID,
		Field:    "_created",
		OldValue: "null",
		NewValue: string(payload),
	}); err != nil {
		return "", EditResult{}, err
	}

	return memberID, EditResult{RecountNeeded: req.EPC != nil && *req.EPC != ""}, nil
}

func fieldTaken(ctx context.Context, store *sqlite.Store, eventID, field string, value any) (bool, error) {
	var one int
	err := store.DB().QueryRowContext(ctx,
		fmt.Sprintf(`SELECT 1 FROM members WHERE event_id = ? AND %s = ? LIMIT 1`, field), // field is hardcoded by callers
		eventID, value).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}
