package publicweb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

//go:embed live.js
var liveScript []byte

type publicFinish struct {
	Club          *string `json:"club"`
	Key           string  `json:"key"`
	RaceID        string  `json:"race_id"`
	RaceName      string  `json:"race_name"`
	Number        *int64  `json:"number"`
	FirstName     string  `json:"first_name"`
	LastName      string  `json:"last_name"`
	Team          *string `json:"team"`
	City          *string `json:"city"`
	FinishTime    int64   `json:"finish_time_ms"`
	CleanTime     string  `json:"clean_time"`
	Gender        *string `json:"gender"`
	GenderPlace   *int    `json:"gender_place"`
	CategoryName  *string `json:"category_name"`
	CategoryPlace *int    `json:"category_place"`
}

type publicFinishes struct {
	StreamVersion int            `json:"stream_version"`
	Published     bool           `json:"published"`
	Filtered      bool           `json:"filtered"`
	EventKey      string         `json:"event_key"`
	EventName     string         `json:"event_name"`
	EventDate     string         `json:"event_date"`
	Races         []raceJSON     `json:"races"`
	Rows          []publicFinish `json:"rows"`
}

func (s *Server) opaqueKey(value string) string {
	mac := hmac.New(sha256.New, s.identityKey[:])
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) handleLiveScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(liveScript)
}

func (s *Server) handleLiveFinishes(w http.ResponseWriter, r *http.Request) {
	eventID := s.published()
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	result, err := s.liveFinishPayload(ctx, eventID, r.URL.Query().Get("race_id"))
	if errors.Is(err, service.ErrLiveFinishRace) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Printf("live finishes: %v", err)
		http.Error(w, "финиши временно недоступны", http.StatusServiceUnavailable)
		return
	}
	if s.published() != eventID {
		result = emptyFinishes()
	}
	writeJSON(w, http.StatusOK, result)
}

func emptyFinishes() publicFinishes {
	return publicFinishes{StreamVersion: 1, Races: make([]raceJSON, 0), Rows: make([]publicFinish, 0)}
}

func (s *Server) liveFinishPayload(ctx context.Context, eventID, raceID string) (publicFinishes, error) {
	result := emptyFinishes()
	if eventID == "" {
		return result, nil
	}
	snapshot, err := s.events.LiveFinishes(ctx, eventID, raceID)
	if err != nil {
		return result, err
	}
	result.Published = true
	result.Filtered = raceID != ""
	result.EventKey = s.opaqueKey("event:" + eventID)
	result.EventName, result.EventDate = snapshot.Event.Name, snapshot.Event.Date
	for _, race := range snapshot.Races {
		result.Races = append(result.Races, raceJSON{ID: race.ID, Name: race.Name, Format: string(race.Format)})
	}
	for _, row := range snapshot.Finishes {
		result.Rows = append(result.Rows, publicFinish{
			Key:    s.opaqueKey("finish:" + eventID + "\x00" + row.MemberID),
			RaceID: row.RaceID, RaceName: row.RaceName, Number: row.Number,
			FirstName: row.FirstName, LastName: row.LastName, Team: row.Team, City: row.City,
			FinishTime: row.FinishTime, CleanTime: row.CleanTime, Club: row.Club,
			Gender: row.Gender, GenderPlace: row.GenderPlace, CategoryName: row.CategoryName, CategoryPlace: row.CategoryPlace,
		})
	}
	return result, nil
}
