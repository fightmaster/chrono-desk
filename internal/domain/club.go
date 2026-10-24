package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// NormalizeClub is shared by source imports, local edits and legacy migration.
func NormalizeClub(club *string) (*string, error) {
	if club == nil {
		return nil, nil
	}
	if !utf8.ValidString(*club) || len(*club) > 255 {
		return nil, fmt.Errorf("invalid Club text")
	}
	value := strings.TrimSpace(*club)
	for _, r := range value {
		if r <= 8 || r >= 14 && r <= 31 || r == 127 || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return nil, fmt.Errorf("invalid Club character")
		}
	}
	if value == "" {
		return nil, nil
	}
	return &value, nil
}
