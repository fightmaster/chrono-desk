package packetissuance

import (
	"errors"
	"time"
)

func ValidateRegistration(row Registration) error {
	if !identifierPattern.MatchString(row.ID) || !identifierPattern.MatchString(row.EventID) || !identifierPattern.MatchString(row.RaceID) ||
		(row.TransferredTo != nil && !identifierPattern.MatchString(*row.TransferredTo)) ||
		(row.Status != "registered" && row.Status != "dns" && row.Status != "dnf" && row.Status != "dsq") {
		return errors.New("invalid_records")
	}
	if row.Reserve && row.Person != nil {
		return errors.New("invalid_records")
	}
	if row.Person != nil {
		return validatePerson(*row.Person)
	}
	return nil
}

func validatePerson(person Person) error {
	if !identifierPattern.MatchString(person.ID) || !validText(person.FirstName, 160) || !validText(person.LastName, 160) ||
		!validText(person.BirthDate, 160) || !validText(person.Gender, 160) || !validText(person.Team, 160) || !validText(person.City, 160) {
		return errors.New("invalid_person")
	}
	if !validReason(person.FirstName+person.LastName) || (person.Gender != "" && person.Gender != "male" && person.Gender != "female") {
		return errors.New("invalid_person")
	}
	if person.BirthDate != "" {
		parsed, err := time.Parse("2006-01-02", person.BirthDate)
		if err != nil || !datePattern.MatchString(person.BirthDate) || parsed.Format("2006-01-02") != person.BirthDate {
			return errors.New("invalid_person")
		}
	}
	return nil
}

func Apply(records []Registration, command Command) ([]Change, error) {
	rows := make(map[string]Registration, len(records))
	for _, row := range records {
		if err := ValidateRegistration(row); err != nil {
			return nil, err
		}
		if _, exists := rows[row.ID]; exists {
			return nil, errors.New("invalid_records")
		}
		rows[row.ID] = row
	}
	source, ok := rows[command.RegistrationID]
	if !ok {
		return nil, errors.New("registration_not_found")
	}
	after := cloneRegistration(source)
	switch command.Type {
	case "issue":
		if err := requireAssigned(source); err != nil {
			return nil, err
		}
		after.Issued = true
	case "undo_issue":
		if !validReason(command.Reason) {
			return nil, errors.New("reason_required")
		}
		after.Issued = false
	case "set_dns":
		if command.Value == nil {
			return nil, errors.New("invalid_command")
		}
		if err := requireEditable(source); err != nil {
			return nil, err
		}
		if err := requireAssigned(source); err != nil {
			return nil, err
		}
		if *command.Value {
			after.Status = "dns"
		} else {
			after.Status = "registered"
		}
	case "edit_person":
		if err := requireEditable(source); err != nil {
			return nil, err
		}
		if err := requireAssigned(source); err != nil {
			return nil, err
		}
		person := *after.Person
		for field, value := range command.Fields {
			switch field {
			case "firstName":
				person.FirstName = value
			case "lastName":
				person.LastName = value
			case "birthDate":
				person.BirthDate = value
			case "gender":
				person.Gender = value
			case "team":
				person.Team = value
			case "city":
				person.City = value
			default:
				return nil, errors.New("invalid_person")
			}
		}
		if err := validatePerson(person); err != nil {
			return nil, err
		}
		after.Person = &person
	case "replace_person":
		if err := requireEditable(source); err != nil {
			return nil, err
		}
		if command.Person == nil || command.IssuePacket == nil {
			return nil, errors.New("invalid_command")
		}
		if err := validatePerson(*command.Person); err != nil {
			return nil, err
		}
		if source.Person != nil && source.Person.ID == command.Person.ID {
			return nil, errors.New("person_identity_reused")
		}
		person := *command.Person
		after.Person = &person
		after.Reserve = false
		after.Issued = *command.IssuePacket
		after.Status = "registered"
	case "move_race":
		if err := requireEditable(source); err != nil {
			return nil, err
		}
		if err := requireAssigned(source); err != nil {
			return nil, err
		}
		target, ok := rows[command.TargetID]
		if !ok || command.IssuePacket == nil || command.TargetID == source.ID || target.EventID != source.EventID || target.RaceID == source.RaceID {
			return nil, errors.New("invalid_target")
		}
		if !target.Reserve || target.Issued || target.HasTimingEvidence || target.Status != "registered" || target.TransferredTo != nil {
			return nil, errors.New("target_not_available")
		}
		after.Status = "dns"
		targetID := target.ID
		after.TransferredTo = &targetID
		targetAfter := cloneRegistration(target)
		targetAfter.Person = clonePerson(source.Person)
		targetAfter.Reserve = false
		targetAfter.Issued = *command.IssuePacket
		return []Change{change(source, after), change(target, targetAfter)}, nil
	default:
		return nil, errors.New("invalid_command")
	}
	if equalRegistration(source, after) {
		return []Change{}, nil
	}
	return []Change{change(source, after)}, nil
}

// ReverseMove derives a correction only from the immutable original operation.
// A request-provided before image is never sufficient historical evidence.
func ReverseMove(records []Registration, original []Change, reason string) ([]Change, error) {
	if !validReason(reason) || len(original) != 2 {
		return nil, errors.New("correction_review_required")
	}
	source, target := original[0], original[1]
	if source.After.TransferredTo == nil || *source.After.TransferredTo != target.RegistrationID ||
		source.After.Status != "dns" || !target.Before.Reserve || source.RegistrationID == target.RegistrationID {
		return nil, errors.New("correction_review_required")
	}
	issuePacket := target.After.Issued
	replayed, err := Apply([]Registration{source.Before, target.Before}, Command{
		Type: "move_race", RegistrationID: source.RegistrationID,
		TargetID: target.RegistrationID, IssuePacket: &issuePacket,
	})
	if err != nil || !equalChanges(replayed, original) {
		return nil, errors.New("correction_review_required")
	}
	current := make(map[string]Registration, len(records))
	for _, row := range records {
		if err := ValidateRegistration(row); err != nil {
			return nil, errors.New("correction_review_required")
		}
		current[row.ID] = row
	}
	changes := make([]Change, 0, 2)
	for _, previous := range original {
		row, ok := current[previous.RegistrationID]
		if !ok || row.HasTimingEvidence || !equalRegistration(row, previous.After) ||
			row.ID != previous.Before.ID || row.EventID != previous.Before.EventID ||
			row.RaceID != previous.Before.RaceID || row.Bib != previous.Before.Bib || row.EPC != previous.Before.EPC {
			return nil, errors.New("correction_review_required")
		}
		changes = append(changes, change(row, previous.Before))
	}
	return changes, nil
}

func requireEditable(row Registration) error {
	if row.HasTimingEvidence {
		return errors.New("timing_review_required")
	}
	if row.Status != "registered" && row.Status != "dns" {
		return errors.New("status_review_required")
	}
	if row.TransferredTo != nil {
		return errors.New("transfer_review_required")
	}
	return nil
}
func requireAssigned(row Registration) error {
	if row.Reserve {
		return errors.New("reserve_slot")
	}
	if row.Person == nil {
		return errors.New("missing_person")
	}
	return nil
}
func clonePerson(person *Person) *Person {
	if person == nil {
		return nil
	}
	copy := *person
	return &copy
}
func cloneRegistration(row Registration) Registration {
	copy := row
	copy.Person = clonePerson(row.Person)
	if row.TransferredTo != nil {
		target := *row.TransferredTo
		copy.TransferredTo = &target
	}
	return copy
}
func change(before, after Registration) Change {
	return Change{RegistrationID: before.ID, Before: cloneRegistration(before), After: cloneRegistration(after)}
}
