package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func decodeClubMetadata(raw json.RawMessage) (*string, error) {
	var club *string
	if !utf8.Valid(raw) || !validClubJSONSurrogates(raw) {
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

// encoding/json replaces lone UTF-16 escapes with U+FFFD. Reject them instead
// so Club data has the same strict meaning as the PHP and browser consumers.
func validClubJSONSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if i+1 >= len(raw) {
			return false
		}
		if raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
		if err != nil {
			return false
		}
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+12 > len(raw) || string(raw[i+6:i+8]) != `\u` {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+8:i+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
		} else {
			i += 5
		}
	}
	return true
}
