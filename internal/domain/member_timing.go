package domain

import timing "gitlab.com/fightmaster1/timing-core"

// CoreMember keeps assigned input distinct from the persisted chip projection.
func CoreMember(race Race, member Member) timing.Member[string] {
	result := timing.Member[string]{ID: member.ID, RaceID: member.RaceID, StartTimeMs: member.StartTimeMs, StartTimeSource: member.StartTimeSource, StartObservationID: member.StartObservationID, FinishTimeMs: member.FinishTimeMs, RaceStartedAtMs: race.StartedAtMs}
	if race.TimingBasis.Dual() {
		result.TimingBasis = race.TimingBasis
		result.AssignedStartTimeMs = member.StartTimeMs
		result.ChipStartTimeMs = member.ChipStartTimeMs
		result.ChipStartSource = member.ChipStartSource
		result.ChipStartObservationID = member.ChipStartObservationID
	}
	return result
}
