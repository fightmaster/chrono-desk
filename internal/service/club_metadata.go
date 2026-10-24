package service

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func decodeClubMetadata(raw json.RawMessage) (*string, error) {
	var club *string
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("неверная кодировка клуба")
	}
	if err := json.Unmarshal(raw, &club); err != nil {
		return nil, fmt.Errorf("неверное значение клуба")
	}
	value, err := domain.NormalizeClub(club)
	if err != nil {
		return nil, fmt.Errorf("название клуба: не более 255 байт без управляющих символов")
	}
	return value, nil
}
