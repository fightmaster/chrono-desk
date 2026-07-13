package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// Excel protocol export. Columns and formatting mirror run5's
// RaceResultsExportRowBuilder so printed protocols look identical to the
// site's: Абс | М/Ж | Кат | Номер | Фамилия | Имя | Дата рождения | Пол |
// Категория | Команда | Город | Статус | Время | Отставание | Очки.
// Очки stays empty offline (points tables are site-side configuration).

var protocolHeaders = []string{
	"Абс", "М/Ж", "Кат", "Номер", "Фамилия", "Имя", "Дата рождения",
	"Пол", "Категория", "Команда", "Город", "Статус", "Время", "Отставание", "Очки",
}

var statusLabels = map[string]string{
	"dns": "Не стартовал",
	"dnf": "Не финишировал",
	"dq":  "Дисквалифицирован",
}

// BuildProtocolXLSX renders the ranked protocol as an .xlsx; returns the file
// bytes and a suggested filename.
func BuildProtocolXLSX(ctx context.Context, store ProtocolStore, raceID string) ([]byte, string, error) {
	protocol, err := BuildProtocol(ctx, store, raceID)
	if err != nil {
		return nil, "", err
	}

	f := excelize.NewFile()
	defer f.Close() //nolint:errcheck
	sheet := f.GetSheetName(0)

	for col, h := range protocolHeaders {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return nil, "", fmt.Errorf("write header: %w", err)
		}
	}
	if style, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}}); err == nil {
		last, _ := excelize.CoordinatesToCellName(len(protocolHeaders), 1)
		_ = f.SetCellStyle(sheet, "A1", last, style)
	}

	// The winner's clean time anchors the "Отставание" column.
	var winnerCleanMs *int64
	for _, r := range protocol.Rows {
		if r.Place != nil && *r.Place == 1 && r.CleanTimeMs != nil {
			winnerCleanMs = r.CleanTimeMs
			break
		}
	}

	for i, r := range protocol.Rows {
		values := []any{
			intOrEmpty(r.Place),
			intOrEmpty(r.GenderPlace),
			intOrEmpty(r.CategoryPlace),
			int64OrEmpty(r.Number),
			r.LastName,
			r.FirstName,
			formatDOB(r.DOB),
			genderTitle(r.Gender),
			strOrEmpty(r.CategoryName),
			strOrEmpty(r.Team),
			strOrEmpty(r.City),
			statusLabel(r.Status),
			cleanTimeCell(r),
			gapCell(r, winnerCleanMs),
			"", // Очки — site-side points config
		}
		for col, v := range values {
			cell, _ := excelize.CoordinatesToCellName(col+1, i+2)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return nil, "", fmt.Errorf("write row %d: %w", i+1, err)
			}
		}
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, "", fmt.Errorf("encode xlsx: %w", err)
	}

	name := fmt.Sprintf("protocol-%s-%s.xlsx", safeFileName(protocol.RaceName), time.Now().Format("2006-01-02"))
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
		return *gender
	}
}

// cleanTimeCell: status rows render an empty time, like run5.
func cleanTimeCell(r ProtocolRow) string {
	if r.Status != "ok" || r.CleanTime == nil {
		return ""
	}
	return *r.CleanTime
}

// gapCell ports run5's DurationTrait: gap to the winner, "+MM:SS.mmm" or
// "+HH:MM:SS.mmm" when hours are present; empty for the winner and non-ok rows.
func gapCell(r ProtocolRow, winnerCleanMs *int64) string {
	if r.Status != "ok" || r.CleanTimeMs == nil || winnerCleanMs == nil {
		return ""
	}
	gap := *r.CleanTimeMs - *winnerCleanMs
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
	if t, err := time.Parse("2006-01-02", *dob); err == nil {
		return t.Format("02.01.2006")
	}
	return *dob
}

func intOrEmpty(v *int) any {
	if v == nil {
		return ""
	}
	return *v
}

func int64OrEmpty(v *int64) any {
	if v == nil {
		return ""
	}
	return *v
}

func strOrEmpty(v *string) any {
	if v == nil {
		return ""
	}
	return *v
}

func safeFileName(s string) string {
	s = strings.TrimSpace(s)
	return unsafeFileChars.ReplaceAllString(s, "_")
}
