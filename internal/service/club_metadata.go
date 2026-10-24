package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

func decodeClubMetadata(raw json.RawMessage) (*string, error) {
	var club *string
	if err := json.Unmarshal(raw, &club); err != nil {
		return nil, fmt.Errorf("неверное значение клуба")
	}
	if club == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*club)
	if len(value) > 255 {
		return nil, fmt.Errorf("слишком длинное название клуба")
	}
	for _, r := range value {
		if r <= 8 || r >= 14 && r <= 31 || r == 127 || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return nil, fmt.Errorf("недопустимый символ в названии клуба")
		}
	}
	return &value, nil
}
