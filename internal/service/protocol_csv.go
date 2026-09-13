package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Protocol CSV columns mirror run5's RaceResultsExportRowBuilder:
// Абс | М/Ж | Кат | Номер | Фамилия | Имя | Дата рождения | Пол |
// Категория | Команда | Город | Статус | Время | Отставание | Очки.
// Очки stays empty offline because points tables are site-side configuration.
var protocolHeaders = []string{
	"Абс", "М/Ж", "Кат", "Номер", "Фамилия", "Имя", "Дата рождения",
	"Пол", "Категория", "Команда", "Город", "Статус", "Время", "Отставание", "Очки",
}

var statusLabels = map[string]string{
	"dns": "Не стартовал",
	"dnf": "Не финишировал",
	"dq":  "Дисквалифицирован",
}

var unsafeExportFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// BuildProtocolCSV renders the ranked protocol as semicolon-delimited UTF-8
// with a BOM and CRLF line endings. That shape opens correctly in common
// spreadsheet applications without requiring an XLSX parser at runtime.
func BuildProtocolCSV(ctx context.Context, store ProtocolStore, raceID string) ([]byte, string, error) {
	protocol, err := BuildProtocol(ctx, store, raceID)
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	w.UseCRLF = true
	if err := w.Write(protocolHeaders); err != nil {
		return nil, "", fmt.Errorf("write CSV header: %w", err)
	}

	var winnerCleanMs *int64
	for _, row := range protocol.Rows {
		if row.Place != nil && *row.Place == 1 && row.CleanTimeMs != nil {
			winnerCleanMs = row.CleanTimeMs
			break
		}
	}

	for i, row := range protocol.Rows {
		values := []string{
			intString(row.Place),
			intString(row.GenderPlace),
			intString(row.CategoryPlace),
			int64String(row.Number),
			safeSpreadsheetText(row.LastName),
			safeSpreadsheetText(row.FirstName),
			formatDOB(row.DOB),
			genderTitle(row.Gender),
			safeSpreadsheetText(strOrEmpty(row.CategoryName)),
			safeSpreadsheetText(strOrEmpty(row.Team)),
			safeSpreadsheetText(strOrEmpty(row.City)),
			statusLabel(row.Status),
			cleanTimeCell(row),
			gapCell(row, winnerCleanMs),
			"", // Очки — site-side points config
		}
		if err := w.Write(values); err != nil {
			return nil, "", fmt.Errorf("write CSV row %d: %w", i+1, err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, "", fmt.Errorf("encode CSV: %w", err)
	}

	name := fmt.Sprintf("protocol-%s-%s.csv", safeFileName(protocol.RaceName), time.Now().Format("2006-01-02"))
	return buf.Bytes(), name, nil
}

func statusLabel(status string) string {
	return statusLabels[status] // "" for ok
}

func genderTitle(gender *string) string {
	switch {
	case gender == nil:
		return ""
	case *gender == "male":
		return "М"
	case *gender == "female":
		return "Ж"
	default:
		return safeSpreadsheetText(*gender)
	}
}

// cleanTimeCell: status rows render an empty time, like run5.
func cleanTimeCell(row ProtocolRow) string {
	if row.Status != "ok" || row.CleanTime == nil {
		return ""
	}
	return *row.CleanTime
}

// gapCell ports run5's DurationTrait: gap to the winner, "+MM:SS.mmm" or
// "+HH:MM:SS.mmm" when hours are present; empty for the winner and non-ok rows.
func gapCell(row ProtocolRow, winnerCleanMs *int64) string {
	if row.Status != "ok" || row.CleanTimeMs == nil || winnerCleanMs == nil {
		return ""
	}
	gap := *row.CleanTimeMs - *winnerCleanMs
	if gap <= 0 {
		return ""
	}
	return "+" + formatGapMillis(gap)
}

func formatGapMillis(millis int64) string {
	seconds := millis / 1000
	ms := millis % 1000
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, secs, ms)
	}
	return fmt.Sprintf("%02d:%02d.%03d", minutes, secs, ms)
}

// formatDOB converts the contract's ISO date to run5's d.m.Y export form.
func formatDOB(dob *string) string {
	if dob == nil || *dob == "" {
		return ""
	}
	if parsed, err := time.Parse("2006-01-02", *dob); err == nil {
		return parsed.Format("02.01.2006")
	}
	return safeSpreadsheetText(*dob)
}

func intString(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func int64String(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func strOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// Spreadsheet applications may evaluate cells beginning with these characters
// as formulas. Prefix user-provided text so an exported protocol stays data.
func safeSpreadsheetText(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	if strings.ContainsRune("=+-@", first) {
		return "'" + value
	}
	return value
}

func safeFileName(value string) string {
	value = strings.TrimSpace(value)
	return unsafeExportFileChars.ReplaceAllString(value, "_")
}
