package packetissuance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	uuidPattern            = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	identifierPattern      = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,128}$`)
	datePattern            = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	sourceCodePattern      = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	canonicalCursorPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})$`)
	timestampPattern       = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`)
)

func ParseBatch(data []byte) ([]Operation, error) {
	root, err := decodeValue(data)
	if err != nil {
		return nil, errors.New("invalid_operation_batch")
	}
	obj, ok := root.(map[string]any)
	if !ok || !exactKeys(obj, "schemaVersion", "operations") || integer(obj["schemaVersion"]) != 1 {
		return nil, errors.New("invalid_operation_batch")
	}
	items, ok := obj["operations"].([]any)
	if !ok || len(items) < 1 || len(items) > 64 {
		return nil, errors.New("invalid_operation_batch")
	}
	operations := make([]Operation, 0, len(items))
	for _, item := range items {
		canonical, err := canonicalJSON(item)
		if err != nil {
			return nil, errors.New("invalid_operation")
		}
		operation, err := parseOperation(item, canonical, false)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

// ParseOperation validates one stored/feed operation using the same strict
// boundary as an uploaded batch.
func ParseOperation(data []byte) (Operation, error) {
	return parseOperationJSON(data, false)
}

// ParseTrustedOperation accepts operation variants that may only arrive from
// an authenticated feed. In particular, schema 2 conflict resolutions are not
// valid tablet uploads and must never pass ParseOperation or ParseBatch.
func ParseTrustedOperation(data []byte) (Operation, error) {
	return parseOperationJSON(data, true)
}

func parseOperationJSON(data []byte, allowResolution bool) (Operation, error) {
	value, err := decodeValue(data)
	if err != nil {
		return Operation{}, errors.New("invalid_operation")
	}
	canonical, err := canonicalJSON(value)
	if err != nil {
		return Operation{}, errors.New("invalid_operation")
	}
	return parseOperation(value, canonical, allowResolution)
}

func ParseBootstrap(data []byte) (Bootstrap, error) {
	root, err := decodeValue(data)
	if err != nil {
		return Bootstrap{}, errors.New("invalid_bootstrap")
	}
	obj, ok := root.(map[string]any)
	version := integer(obj["schemaVersion"])
	keysOK := version == 1 && exactKeys(obj, "schemaVersion", "scopeId", "sourceKind", "event", "races", "registrations", "baselineId") ||
		version == 2 && exactKeys(obj, "schemaVersion", "scopeId", "sourceKind", "event", "races", "registrations", "baselineId", "feedCursor")
	if !ok || !keysOK || stringValue(obj["sourceKind"]) != "site" ||
		!identifierPattern.MatchString(stringValue(obj["scopeId"])) || !identifierPattern.MatchString(stringValue(obj["baselineId"])) {
		return Bootstrap{}, errors.New("invalid_bootstrap")
	}
	feedAfter := "0"
	if version == 2 {
		var valid bool
		feedAfter, _, valid = feedCursor(obj["feedCursor"])
		if !valid {
			return Bootstrap{}, errors.New("invalid_bootstrap")
		}
	}
	eventObj, ok := obj["event"].(map[string]any)
	if !ok || !exactKeys(eventObj, "id", "name", "date") {
		return Bootstrap{}, errors.New("invalid_bootstrap")
	}
	event := Event{ID: stringValue(eventObj["id"]), Name: stringValue(eventObj["name"]), Date: stringValue(eventObj["date"])}
	if !identifierPattern.MatchString(event.ID) || !validText(event.Name, 512) || !validDate(event.Date) {
		return Bootstrap{}, errors.New("invalid_bootstrap")
	}
	rawRaces, racesOK := obj["races"].([]any)
	rawRows, rowsOK := obj["registrations"].([]any)
	if !racesOK || !rowsOK || len(rawRaces) > 1000 || len(rawRows) > 20000 {
		return Bootstrap{}, errors.New("invalid_bootstrap")
	}
	races := make([]Race, 0, len(rawRaces))
	raceIDs := make(map[string]bool, len(rawRaces))
	for _, raw := range rawRaces {
		raceObj, ok := raw.(map[string]any)
		if !ok || !exactKeys(raceObj, "id", "name") {
			return Bootstrap{}, errors.New("invalid_bootstrap")
		}
		race := Race{ID: stringValue(raceObj["id"]), Name: stringValue(raceObj["name"])}
		if !identifierPattern.MatchString(race.ID) || !validText(race.Name, 256) || raceIDs[race.ID] {
			return Bootstrap{}, errors.New("invalid_bootstrap")
		}
		raceIDs[race.ID] = true
		races = append(races, race)
	}
	rows := make([]Registration, 0, len(rawRows))
	rowIDs := make(map[string]bool, len(rawRows))
	for _, raw := range rawRows {
		row, err := parseRegistration(raw)
		if err != nil || row.EventID != event.ID || !raceIDs[row.RaceID] || rowIDs[row.ID] {
			return Bootstrap{}, errors.New("invalid_bootstrap")
		}
		rowIDs[row.ID] = true
		rows = append(rows, row)
	}
	for _, row := range rows {
		if row.TransferredTo != nil && (*row.TransferredTo == row.ID || !rowIDs[*row.TransferredTo]) {
			return Bootstrap{}, errors.New("invalid_bootstrap")
		}
	}
	return Bootstrap{
		SchemaVersion: int(version), ScopeID: stringValue(obj["scopeId"]), SourceKind: "site",
		Event: event, Races: races, Registrations: rows, BaselineID: stringValue(obj["baselineId"]), FeedCursor: feedAfter,
	}, nil
}

func ParseFeedPage(data []byte, scopeID, expectedAfter string) (FeedPage, error) {
	root, err := decodeValue(data)
	obj, ok := root.(map[string]any)
	if err != nil || !ok || !exactKeys(obj, "schemaVersion", "scopeId", "cursor", "actions") ||
		integer(obj["schemaVersion"]) != 1 || stringValue(obj["scopeId"]) != scopeID {
		return FeedPage{}, errors.New("invalid_feed_response")
	}
	cursorObj, ok := obj["cursor"].(map[string]any)
	if !ok || !exactKeys(cursorObj, "after", "next", "head", "hasMore") {
		return FeedPage{}, errors.New("invalid_feed_response")
	}
	after, afterNumber, afterOK := feedCursor(cursorObj["after"])
	next, nextNumber, nextOK := feedCursor(cursorObj["next"])
	head, headNumber, headOK := feedCursor(cursorObj["head"])
	hasMore, moreOK := cursorObj["hasMore"].(bool)
	if !afterOK || !nextOK || !headOK || !moreOK || after != expectedAfter || afterNumber > nextNumber ||
		nextNumber > headNumber || hasMore != (nextNumber < headNumber) {
		return FeedPage{}, errors.New("invalid_feed_response")
	}
	eventID := feedEventID(scopeID)
	rawActions, ok := obj["actions"].([]any)
	if eventID == "" || !ok || len(rawActions) > 100 {
		return FeedPage{}, errors.New("invalid_feed_response")
	}
	actions := make([]FeedAction, 0, len(rawActions))
	seen := make(map[string]bool, len(rawActions))
	expectedSequence := afterNumber
	for _, raw := range rawActions {
		expectedSequence++
		action, err := parseFeedAction(raw, scopeID, eventID, expectedSequence)
		if err != nil || seen[action.ActionID] {
			return FeedPage{}, errors.New("invalid_feed_response")
		}
		seen[action.ActionID] = true
		actions = append(actions, action)
	}
	if expectedSequence != nextNumber || (len(actions) == 0 && next != after) {
		return FeedPage{}, errors.New("invalid_feed_response")
	}
	return FeedPage{SchemaVersion: 1, ScopeID: scopeID,
		Cursor: FeedCursor{After: after, Next: next, Head: head, HasMore: hasMore}, Actions: actions}, nil
}

func parseFeedAction(value any, scopeID, eventID string, expectedSequence int64) (FeedAction, error) {
	obj, ok := value.(map[string]any)
	if !ok || !exactKeys(obj, "actionId", "kind", "sequence", "recordedAt", "sourceCode", "outcome", "code", "operation", "changes") {
		return FeedAction{}, errors.New("invalid")
	}
	sequence, sequenceNumber, sequenceOK := feedCursor(obj["sequence"])
	action := FeedAction{ActionID: stringValue(obj["actionId"]), Kind: stringValue(obj["kind"]),
		Sequence: sequence, RecordedAt: stringValue(obj["recordedAt"]), SourceCode: stringValue(obj["sourceCode"]),
		Outcome: stringValue(obj["outcome"])}
	if !uuidPattern.MatchString(action.ActionID) || !sequenceOK || sequenceNumber != expectedSequence ||
		!validTimestamp(action.RecordedAt) || !sourceCodePattern.MatchString(action.SourceCode) {
		return FeedAction{}, errors.New("invalid")
	}
	if obj["code"] != nil {
		code, ok := obj["code"].(string)
		if !ok || !sourceCodePattern.MatchString(code) {
			return FeedAction{}, errors.New("invalid")
		}
		action.Code = &code
	}
	if (action.Outcome == "conflict") != (action.Code != nil) ||
		(action.Outcome != "applied" && action.Outcome != "equivalent" && action.Outcome != "conflict") {
		return FeedAction{}, errors.New("invalid")
	}
	rawChanges, ok := obj["changes"].([]any)
	if !ok || len(rawChanges) > 20000 {
		return FeedAction{}, errors.New("invalid")
	}
	ids := make(map[string]bool, len(rawChanges))
	for _, raw := range rawChanges {
		change, err := parseFeedChange(raw, eventID)
		if err != nil || ids[change.RegistrationID] {
			return FeedAction{}, errors.New("invalid")
		}
		ids[change.RegistrationID] = true
		action.Changes = append(action.Changes, change)
	}
	if action.Kind == "operation" {
		rawOperation, err := canonicalJSON(obj["operation"])
		if err != nil {
			return FeedAction{}, errors.New("invalid")
		}
		operation, err := ParseTrustedOperation(rawOperation)
		if err != nil || operation.OperationID != action.ActionID || operation.ScopeID != scopeID ||
			(action.Outcome == "applied") != (len(action.Changes) > 0) {
			return FeedAction{}, errors.New("invalid")
		}
		action.Operation = &operation
		if operation.SchemaVersion == 2 {
			if action.Outcome == "equivalent" ||
				(action.Outcome == "applied" && !resolutionFeedChangesMatch(operation.Changes, action.Changes)) {
				return FeedAction{}, errors.New("invalid")
			}
		}
	} else if action.Kind != "server_change" || obj["operation"] != nil || action.Outcome != "applied" || len(action.Changes) == 0 {
		return FeedAction{}, errors.New("invalid")
	}
	canonical, err := canonicalJSON(value)
	if err != nil {
		return FeedAction{}, errors.New("invalid")
	}
	action.canonical = canonical
	return action, nil
}

func resolutionFeedChangesMatch(operation []Change, feed []FeedChange) bool {
	if len(operation) != len(feed) {
		return false
	}
	for index, expected := range operation {
		actual := feed[index]
		if actual.Before == nil || actual.RegistrationID != expected.RegistrationID {
			return false
		}
		before, after := expected.Before, expected.After
		before.HasTimingEvidence, after.HasTimingEvidence = false, false
		if !reflect.DeepEqual(before, *actual.Before) || (IsVirtualRegistration(expected.After) && actual.After != nil) ||
			(!IsVirtualRegistration(expected.After) && (actual.After == nil || !reflect.DeepEqual(after, *actual.After))) {
			return false
		}
	}
	return true
}

func parseFeedChange(value any, eventID string) (FeedChange, error) {
	obj, ok := value.(map[string]any)
	if !ok || !exactKeys(obj, "registrationId", "before", "after") {
		return FeedChange{}, errors.New("invalid")
	}
	id := stringValue(obj["registrationId"])
	if !identifierPattern.MatchString(id) || (obj["before"] == nil && obj["after"] == nil) {
		return FeedChange{}, errors.New("invalid")
	}
	change := FeedChange{RegistrationID: id}
	parseSide := func(raw any) (*Registration, error) {
		if raw == nil {
			return nil, nil
		}
		row, err := parseFeedRegistration(raw)
		if err != nil || row.ID != id || row.EventID != eventID {
			return nil, errors.New("invalid")
		}
		return &row, nil
	}
	var err error
	change.Before, err = parseSide(obj["before"])
	if err != nil {
		return FeedChange{}, err
	}
	change.After, err = parseSide(obj["after"])
	if err != nil {
		return FeedChange{}, errors.New("invalid")
	}
	return change, nil
}

func parseFeedRegistration(value any) (Registration, error) {
	obj, ok := value.(map[string]any)
	if !ok || !exactKeys(obj, "id", "eventId", "raceId", "bib", "epc", "person", "reserve", "issued", "status", "transferredTo") {
		return Registration{}, errors.New("invalid")
	}
	copy := make(map[string]any, len(obj)+1)
	for key, raw := range obj {
		copy[key] = raw
	}
	copy["hasTimingEvidence"] = false
	return parseRegistration(copy)
}

func feedCursor(value any) (string, int64, bool) {
	text, ok := value.(string)
	if !ok || !canonicalCursorPattern.MatchString(text) {
		return "", 0, false
	}
	number, err := strconv.ParseInt(text, 10, 64)
	return text, number, err == nil
}

func feedEventID(scopeID string) string {
	parts := strings.Split(scopeID, ":")
	if len(parts) != 3 || parts[0] != "site" || !identifierPattern.MatchString(parts[2]) {
		return ""
	}
	return parts[2]
}

func validTimestamp(value string) bool {
	if !timestampPattern.MatchString(value) {
		return false
	}
	_, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	return err == nil
}

func ContentHash(operation Operation) string {
	sum := sha256.Sum256(operation.canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func parseOperation(value any, canonical []byte, allowResolution bool) (Operation, error) {
	obj, ok := value.(map[string]any)
	required := []string{"schemaVersion", "operationId", "scopeId", "baselineId", "originInstanceId", "originSequence", "createdAtMs", "claimedActor", "command", "bases", "changes"}
	if !ok || !(exactKeys(obj, required...) || exactKeys(obj, append(required, "note")...)) {
		return Operation{}, errors.New("invalid_operation")
	}
	sequence, seqOK := safeInteger(obj["originSequence"], true)
	created, createdOK := safeInteger(obj["createdAtMs"], false)
	note, noteOK := optionalText(obj, "note", 500)
	operation := Operation{
		SchemaVersion: integer(obj["schemaVersion"]), OperationID: stringValue(obj["operationId"]),
		ScopeID: stringValue(obj["scopeId"]), BaselineID: stringValue(obj["baselineId"]),
		OriginInstanceID: stringValue(obj["originInstanceId"]), OriginSequence: sequence,
		CreatedAtMs: created, ClaimedActor: stringValue(obj["claimedActor"]), Note: note, canonical: canonical,
	}
	if (operation.SchemaVersion != 1 && !(allowResolution && operation.SchemaVersion == 2)) || !uuidPattern.MatchString(operation.OperationID) ||
		!identifierPattern.MatchString(operation.ScopeID) || !identifierPattern.MatchString(operation.BaselineID) ||
		!uuidPattern.MatchString(operation.OriginInstanceID) || !seqOK || !createdOK ||
		!validText(operation.ClaimedActor, 160) || !noteOK {
		return Operation{}, errors.New("invalid_operation")
	}
	command, err := parseCommand(obj["command"], allowResolution && operation.SchemaVersion == 2)
	if err != nil {
		return Operation{}, err
	}
	operation.Command = command
	bases, ok := obj["bases"].([]any)
	changes, changesOK := obj["changes"].([]any)
	if !ok || !changesOK || (len(changes) != 1 && len(changes) != 2) || len(bases) != len(changes) {
		return Operation{}, errors.New("invalid_operation")
	}
	ids := make(map[string]bool, len(changes))
	before := make([]Registration, 0, len(changes))
	for i := range changes {
		base, err := parseBase(bases[i])
		if err != nil {
			return Operation{}, errors.New("invalid_operation_changes")
		}
		change, err := parseChange(changes[i])
		if err != nil || base.RegistrationID != change.RegistrationID || ids[change.RegistrationID] {
			return Operation{}, errors.New("invalid_operation_changes")
		}
		ids[change.RegistrationID] = true
		for _, fieldEqual := range []bool{
			change.Before.ID == change.After.ID, change.Before.EventID == change.After.EventID,
			change.Before.RaceID == change.After.RaceID, change.Before.Bib == change.After.Bib || command.Type == "assign_number" || command.Type == "unassign_number" || command.Type == "create_registration" || operation.SchemaVersion == 2,
			change.Before.EPC == change.After.EPC,
			change.Before.HasTimingEvidence == change.After.HasTimingEvidence,
			change.Before.ID == change.RegistrationID,
		} {
			if !fieldEqual {
				return Operation{}, errors.New("invalid_operation_changes")
			}
		}
		operation.Bases = append(operation.Bases, base)
		operation.Changes = append(operation.Changes, change)
		before = append(before, change.Before)
	}
	for _, row := range before[1:] {
		if row.EventID != before[0].EventID {
			return Operation{}, errors.New("invalid_operation_changes")
		}
	}
	if operation.SchemaVersion == 2 {
		if command.Type != "resolve_conflict" || command.Inputs[0] == operation.OperationID || len(operation.Bases) != len(operation.Changes) {
			return Operation{}, errors.New("invalid_operation_changes")
		}
		for _, base := range operation.Bases {
			if len(base.Heads) != 1 || base.Heads[0] != command.Inputs[0] {
				return Operation{}, errors.New("invalid_operation_changes")
			}
		}
	} else if command.Type == "correct_move" {
		if len(changes) != 2 {
			return Operation{}, errors.New("invalid_operation_changes")
		}
	} else {
		expected, err := Apply(before, command)
		if err != nil {
			return Operation{}, errors.New("invalid_operation_command")
		}
		if !equalChanges(expected, operation.Changes) {
			return Operation{}, errors.New("invalid_operation_changes")
		}
	}
	return operation, nil
}

func parseCommand(value any, allowResolution bool) (Command, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return Command{}, errors.New("invalid_operation_command")
	}
	raw, err := canonicalJSON(obj)
	if err != nil {
		return Command{}, errors.New("invalid_operation_command")
	}
	command := Command{Type: stringValue(obj["type"]), RegistrationID: stringValue(obj["registrationId"]), raw: raw}
	switch command.Type {
	case "assign_number", "create_registration":
		keys := []string{"type", "registrationId", "bib", "issuePacket"}
		if command.Type == "create_registration" {
			keys = append(keys, "eventId", "raceId", "person")
		}
		if !identifierPattern.MatchString(command.RegistrationID) || !exactKeys(obj, keys...) {
			return Command{}, errors.New("invalid_operation_command")
		}
		bib, bibOK := obj["bib"].(string)
		issue, issueOK := obj["issuePacket"].(bool)
		if !bibOK || !issueOK {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.Bib, command.IssuePacket = bib, &issue
		if command.Type == "create_registration" {
			command.EventID, command.RaceID = stringValue(obj["eventId"]), stringValue(obj["raceId"])
			person, err := parsePerson(obj["person"])
			if err != nil {
				return Command{}, errors.New("invalid_operation_command")
			}
			command.Person = &person
		}
	case "issue", "unassign_number":
		if !identifierPattern.MatchString(command.RegistrationID) {
			return Command{}, errors.New("invalid_operation_command")
		}
		if !exactKeys(obj, "type", "registrationId") {
			return Command{}, errors.New("invalid_operation_command")
		}
	case "undo_issue":
		if !identifierPattern.MatchString(command.RegistrationID) {
			return Command{}, errors.New("invalid_operation_command")
		}
		if !exactKeys(obj, "type", "registrationId", "reason") {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.Reason = stringValue(obj["reason"])
		if !validReason(command.Reason) {
			return Command{}, errors.New("invalid_operation_command")
		}
	case "set_dns":
		if !identifierPattern.MatchString(command.RegistrationID) {
			return Command{}, errors.New("invalid_operation_command")
		}
		if !exactKeys(obj, "type", "registrationId", "value") {
			return Command{}, errors.New("invalid_operation_command")
		}
		value, ok := obj["value"].(bool)
		if !ok {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.Value = &value
	case "release_to_reserve":
		if !identifierPattern.MatchString(command.RegistrationID) || !exactKeys(obj, "type", "registrationId") {
			return Command{}, errors.New("invalid_operation_command")
		}
	case "edit_person":
		if !identifierPattern.MatchString(command.RegistrationID) {
			return Command{}, errors.New("invalid_operation_command")
		}
		if !exactKeys(obj, "type", "registrationId", "fields") {
			return Command{}, errors.New("invalid_operation_command")
		}
		fields, ok := obj["fields"].(map[string]any)
		if !ok || len(fields) == 0 {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.Fields = make(map[string]string, len(fields))
		for key, rawValue := range fields {
			if !personField(key) {
				return Command{}, errors.New("invalid_operation_command")
			}
			text, ok := rawValue.(string)
			if !ok {
				return Command{}, errors.New("invalid_operation_command")
			}
			command.Fields[key] = text
		}
	case "replace_person":
		if !identifierPattern.MatchString(command.RegistrationID) {
			return Command{}, errors.New("invalid_operation_command")
		}
		if !exactKeys(obj, "type", "registrationId", "person", "issuePacket") {
			return Command{}, errors.New("invalid_operation_command")
		}
		person, err := parsePerson(obj["person"])
		if err != nil {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.Person = &person
		issue, ok := obj["issuePacket"].(bool)
		if !ok {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.IssuePacket = &issue
	case "move_race", "move_to_reserve":
		if !identifierPattern.MatchString(command.RegistrationID) {
			return Command{}, errors.New("invalid_operation_command")
		}
		if !exactKeys(obj, "type", "registrationId", "targetId", "issuePacket") {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.TargetID = stringValue(obj["targetId"])
		if !identifierPattern.MatchString(command.TargetID) {
			return Command{}, errors.New("invalid_operation_command")
		}
		issue, ok := obj["issuePacket"].(bool)
		if !ok {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.IssuePacket = &issue
	case "correct_move":
		if !exactKeys(obj, "type", "operationId", "reason") {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.RegistrationID = ""
		command.OperationID = stringValue(obj["operationId"])
		command.Reason = stringValue(obj["reason"])
		if !uuidPattern.MatchString(command.OperationID) || !validReason(command.Reason) {
			return Command{}, errors.New("invalid_operation_command")
		}
	case "resolve_conflict":
		if !allowResolution || !exactKeys(obj, "type", "inputs", "keep", "reason") {
			return Command{}, errors.New("invalid_operation_command")
		}
		inputs, inputsOK := obj["inputs"].([]any)
		keep, keepOK := obj["keep"].([]any)
		command.Reason = stringValue(obj["reason"])
		if !inputsOK || !keepOK || len(inputs) != 1 || len(keep) > 1 || !validReason(command.Reason) {
			return Command{}, errors.New("invalid_operation_command")
		}
		input, ok := inputs[0].(string)
		if !ok || !uuidPattern.MatchString(input) {
			return Command{}, errors.New("invalid_operation_command")
		}
		command.Inputs = []string{input}
		if len(keep) == 1 {
			kept, ok := keep[0].(string)
			if !ok || kept != input {
				return Command{}, errors.New("invalid_operation_command")
			}
			command.Keep = []string{kept}
		}
	default:
		return Command{}, errors.New("invalid_operation_command")
	}
	return command, nil
}

func parseBase(value any) (Base, error) {
	obj, ok := value.(map[string]any)
	if !ok || !exactKeys(obj, "registrationId", "heads") {
		return Base{}, errors.New("invalid")
	}
	base := Base{RegistrationID: stringValue(obj["registrationId"])}
	raw, ok := obj["heads"].([]any)
	if !identifierPattern.MatchString(base.RegistrationID) || !ok || len(raw) > 64 {
		return Base{}, errors.New("invalid")
	}
	for _, value := range raw {
		head, ok := value.(string)
		if !ok || !uuidPattern.MatchString(head) || (len(base.Heads) > 0 && base.Heads[len(base.Heads)-1] >= head) {
			return Base{}, errors.New("invalid")
		}
		base.Heads = append(base.Heads, head)
	}
	return base, nil
}

func parseChange(value any) (Change, error) {
	obj, ok := value.(map[string]any)
	if !ok || !exactKeys(obj, "registrationId", "before", "after") {
		return Change{}, errors.New("invalid")
	}
	before, err := parseRegistration(obj["before"])
	if err != nil {
		return Change{}, err
	}
	after, err := parseRegistration(obj["after"])
	if err != nil {
		return Change{}, err
	}
	id := stringValue(obj["registrationId"])
	if !identifierPattern.MatchString(id) {
		return Change{}, errors.New("invalid")
	}
	return Change{RegistrationID: id, Before: before, After: after}, nil
}

func parseRegistration(value any) (Registration, error) {
	obj, ok := value.(map[string]any)
	if !ok || !exactKeys(obj, "id", "eventId", "raceId", "bib", "epc", "person", "reserve", "issued", "status", "transferredTo", "hasTimingEvidence") {
		return Registration{}, errors.New("invalid")
	}
	row := Registration{ID: stringValue(obj["id"]), EventID: stringValue(obj["eventId"]), RaceID: stringValue(obj["raceId"]), Bib: stringValue(obj["bib"]), EPC: stringValue(obj["epc"]), Status: stringValue(obj["status"])}
	var okBool bool
	row.Reserve, okBool = obj["reserve"].(bool)
	if !okBool {
		return Registration{}, errors.New("invalid")
	}
	row.Issued, okBool = obj["issued"].(bool)
	if !okBool {
		return Registration{}, errors.New("invalid")
	}
	row.HasTimingEvidence, okBool = obj["hasTimingEvidence"].(bool)
	if !okBool {
		return Registration{}, errors.New("invalid")
	}
	if obj["person"] != nil {
		person, err := parsePerson(obj["person"])
		if err != nil {
			return Registration{}, err
		}
		row.Person = &person
	}
	if obj["transferredTo"] != nil {
		target, ok := obj["transferredTo"].(string)
		if !ok {
			return Registration{}, errors.New("invalid")
		}
		row.TransferredTo = &target
	}
	if err := ValidateRegistration(row); err != nil {
		return Registration{}, err
	}
	return row, nil
}

func parsePerson(value any) (Person, error) {
	obj, ok := value.(map[string]any)
	if !ok || !exactKeys(obj, "id", "firstName", "lastName", "birthDate", "gender", "team", "city") {
		return Person{}, errors.New("invalid")
	}
	p := Person{ID: stringValue(obj["id"]), FirstName: stringValue(obj["firstName"]), LastName: stringValue(obj["lastName"]), BirthDate: stringValue(obj["birthDate"]), Gender: stringValue(obj["gender"]), Team: stringValue(obj["team"]), City: stringValue(obj["city"])}
	if err := validatePerson(p); err != nil {
		return Person{}, err
	}
	return p, nil
}

func decodeValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing")
	}
	return value, nil
}

func exactKeys(obj map[string]any, keys ...string) bool {
	if len(obj) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := obj[key]; !ok {
			return false
		}
	}
	return true
}
func stringValue(value any) string { valueString, _ := value.(string); return valueString }
func integer(value any) int64 {
	number, ok := value.(json.Number)
	if !ok {
		return -1
	}
	parsed, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil {
		return -1
	}
	return parsed
}
func safeInteger(value any, positive bool) (int64, bool) {
	parsed := integer(value)
	return parsed, parsed >= 0 && parsed <= MaxSafeInteger && (!positive || parsed > 0)
}
func optionalText(obj map[string]any, key string, limit int) (*string, bool) {
	value, exists := obj[key]
	if !exists {
		return nil, true
	}
	text, ok := value.(string)
	return &text, ok && validText(text, limit)
}
func validText(value string, limit int) bool {
	if !utf8.ValidString(value) || len([]byte(value)) > limit {
		return false
	}
	for _, r := range value {
		if (r <= 8) || (r >= 14 && r <= 31) || r == 127 || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return false
		}
	}
	return true
}
func validReason(value string) bool {
	if !validText(value, 500) {
		return false
	}
	for _, r := range value {
		if !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}
func validDate(value string) bool {
	if !datePattern.MatchString(value) {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}
func personField(key string) bool {
	return key == "firstName" || key == "lastName" || key == "birthDate" || key == "gender" || key == "team" || key == "city"
}

func canonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := writeCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func writeCanonical(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		if !validText(typed, int(^uint(0)>>1)) {
			return errors.New("invalid unicode")
		}
		writeJSONString(out, typed)
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil || parsed < 0 || parsed > MaxSafeInteger || strconv.FormatInt(parsed, 10) != string(typed) {
			return errors.New("invalid number")
		}
		out.WriteString(string(typed))
	case []any:
		out.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeJSONString(out, key)
			out.WriteByte(':')
			if err := writeCanonical(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("invalid canonical type %T", value)
	}
	return nil
}
func writeJSONString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}

func equalChanges(a, b []Change) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalChange(a[i], b[i]) {
			return false
		}
	}
	return true
}
func equalChange(a, b Change) bool {
	return a.RegistrationID == b.RegistrationID && equalRegistration(a.Before, b.Before) && equalRegistration(a.After, b.After)
}
func equalRegistration(a, b Registration) bool { return reflect.DeepEqual(a, b) }
