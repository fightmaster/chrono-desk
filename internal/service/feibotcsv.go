package service

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Port of run5's FeibotCsvImporter (app/Services/FeibotCsvImporter.php).
// Line format: EPC:YYYY-MM-DD_HH:MM:SS.mmm,port=X,rssi=Y
// The id formula md5(board+epc+timeMs+port) and the EPC/board normalization
// must stay byte-identical so flash-drive imports dedup against logs already
// collected over TCP — here and on the site.

var feibotRowRE = regexp.MustCompile(`^([0-9a-fA-F]+):(\d{4}-\d{2}-\d{2}_\d{2}:\d{2}:\d{2}\.\d{3}),port=(\d+),rssi=(-?\d+)\s*$`)

const feibotTimeLayout = "2006-01-02_15:04:05.000"

// FeibotImportError describes one rejected CSV line.
type FeibotImportError struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
	Raw    string `json:"raw"`
}

// FeibotImportResult mirrors run5's FeibotImportResult counters.
type FeibotImportResult struct {
	Parsed     int                 `json:"parsed"`
	Inserted   int                 `json:"inserted"`
	Duplicates int                 `json:"duplicates"`
	Errors     []FeibotImportError `json:"errors"`
}

const maxReportedErrors = 20

// FeibotCsvImporter ingests reader flash-drive dumps into an event database.
type FeibotCsvImporter struct {
	store *sqlite.Store
}

func NewFeibotCsvImporter(store *sqlite.Store) *FeibotCsvImporter {
	return &FeibotCsvImporter{store: store}
}

// Import parses the CSV and inserts logs idempotently. deviceCode is the
// reader id (e.g. "U659"); timezone interprets the zoneless timestamps —
// default it from the event export's timezone field.
func (i *FeibotCsvImporter) Import(ctx context.Context, r io.Reader, eventID, deviceCode, timezone string) (FeibotImportResult, error) {
	res := FeibotImportResult{Errors: []FeibotImportError{}}

	if deviceCode == "" {
		return res, fmt.Errorf("device code is required")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return res, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	board := "Feibot:" + deviceCode

	var logs []domain.RfidLog
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		res.Parsed++

		logEntry, perr := parseFeibotLine(line, loc, board, eventID)
		if perr != nil {
			if len(res.Errors) < maxReportedErrors {
				res.Errors = append(res.Errors, FeibotImportError{Line: lineNo, Reason: perr.Error(), Raw: line})
			}
			continue
		}
		logEntry.CaptureSourceID = "chrono-desk:" + eventID + ":" + board
		logs = append(logs, logEntry)
	}
	if err := scanner.Err(); err != nil {
		return res, fmt.Errorf("read csv: %w", err)
	}

	// Content-level dedup against rows already stored for this board, ported
	// from run5's loadExistingKeys: historical site data contains legacy ids
	// that do not follow the md5 formula, so the id PK alone cannot dedup a
	// flash drive against an event export.
	existing, err := i.store.ExistingRfidLogKeys(ctx, eventID, board)
	if err != nil {
		return res, err
	}
	fresh := logs[:0]
	for _, l := range logs {
		key := contentKey(l.EPC, l.TimeMs, l.Ant)
		if existing[key] {
			res.Duplicates++
			continue
		}
		existing[key] = true
		fresh = append(fresh, l)
	}

	inserted, err := i.store.InsertOwnedRfidLogs(ctx, fresh)
	if err != nil {
		return res, err
	}
	res.Inserted = int(inserted)
	res.Duplicates += len(fresh) - res.Inserted
	return res, nil
}

func contentKey(epc string, timeMs int64, ant int) string {
	return epc + "|" + strconv.FormatInt(timeMs, 10) + "|" + strconv.Itoa(ant)
}

func parseFeibotLine(line string, loc *time.Location, board, eventID string) (domain.RfidLog, error) {
	m := feibotRowRE.FindStringSubmatch(line)
	if m == nil {
		return domain.RfidLog{}, fmt.Errorf("malformed line")
	}

	epc := strings.ToUpper(m[1])
	t, err := time.ParseInLocation(feibotTimeLayout, m[2], loc)
	if err != nil {
		return domain.RfidLog{}, fmt.Errorf("invalid time %q: %w", m[2], err)
	}
	timeMs := t.UnixMilli()
	port, err := strconv.Atoi(m[3])
	if err != nil {
		return domain.RfidLog{}, fmt.Errorf("invalid port %q", m[3])
	}
	rssi, err := strconv.Atoi(m[4])
	if err != nil {
		return domain.RfidLog{}, fmt.Errorf("invalid rssi %q", m[4])
	}

	return domain.RfidLog{
		ID:      RfidLogID(board, epc, timeMs, port),
		EventID: eventID,
		TimeMs:  timeMs,
		Ant:     port,
		EPC:     epc,
		RSSI:    rssi,
		Board:   board,
	}, nil
}

// RfidLogID is the cross-system idempotency key: md5(board+epc+timeMs+ant),
// concatenated without separators. Must match rfid-hub's adapters and run5's
// RfidLog — never change unilaterally.
func RfidLogID(board, epc string, timeMs int64, ant int) string {
	sum := md5.Sum([]byte(board + epc + strconv.FormatInt(timeMs, 10) + strconv.Itoa(ant)))
	return hex.EncodeToString(sum[:])
}
