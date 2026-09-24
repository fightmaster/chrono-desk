package packetissuance

// ReserveOrigin records explicit reserve state, independently of EPC presence.
type ReserveOrigin struct {
	RegistrationID string `json:"registrationId"`
	Bib            string `json:"bib"`
	EPC            string `json:"epc"`
	RaceID         string `json:"raceId"`
}

func MatchingReserveOrigins(rows []Registration, candidates []ReserveOrigin) []ReserveOrigin {
	current := make(map[string]Registration, len(rows))
	for _, row := range rows {
		current[row.ID] = row
		if row.Reserve && row.Bib != "" {
			candidates = append(candidates, ReserveOrigin{row.ID, row.Bib, row.EPC, row.RaceID})
		}
	}
	matched := make(map[string]ReserveOrigin)
	for _, origin := range candidates {
		row, ok := current[origin.RegistrationID]
		if ok && origin.Bib != "" && row.EPC == origin.EPC && (row.Bib == origin.Bib || row.Bib == "" && row.EPC != "") {
			matched[row.ID] = origin
		}
	}
	result := make([]ReserveOrigin, 0, len(matched))
	for _, row := range rows {
		if origin, ok := matched[row.ID]; ok {
			result = append(result, origin)
		}
	}
	return result
}
