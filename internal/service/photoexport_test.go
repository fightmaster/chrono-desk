package service

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// readZip returns the archive's entries by name.
func readZip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func TestZipFinishes_bundlesPhotosAndReferencesThemRelatively(t *testing.T) {
	finishes := []MergedFinish{
		{Photo: sqlite.Photo{ID: "a", TimeMs: 2000, Bib: "5", BibSource: "manual", CameraLabel: "cam1", BestPhotoURL: "http://phone-a/best.jpg"}, Cams: []string{"cam1", "cam2"}, MergedCount: 2},
		{Photo: sqlite.Photo{ID: "b", TimeMs: 1000, CameraLabel: "cam1", BestPhotoURL: "http://phone-gone/best.jpg"}, Cams: []string{"cam1"}, MergedCount: 1},
		{Photo: sqlite.Photo{ID: "c", TimeMs: 500, CameraLabel: "cam2", BestPhotoURL: ""}, Cams: []string{"cam2"}, MergedCount: 1},
	}
	// Only phone-a is still reachable; phone-gone errors (left the LAN), c has no URL.
	fetch := func(u string) ([]byte, error) {
		if u == "http://phone-a/best.jpg" {
			return []byte("JPEGBYTES-A"), nil
		}
		return nil, fmt.Errorf("unreachable")
	}

	data, name, err := zipFinishes(finishes, fetch)
	if err != nil {
		t.Fatalf("zipFinishes: %v", err)
	}
	if name != "photo-finishes.zip" {
		t.Fatalf("name = %q, want photo-finishes.zip", name)
	}

	entries := readZip(t, data)
	if _, ok := entries["photos/0001.jpg"]; !ok {
		t.Fatalf("reachable photo not bundled; entries: %v", keys(entries))
	}
	if got := string(entries["photos/0001.jpg"]); got != "JPEGBYTES-A" {
		t.Fatalf("bundled image bytes = %q", got)
	}
	// The unreachable / URL-less finishes must NOT create dangling image entries.
	if _, ok := entries["photos/0002.jpg"]; ok {
		t.Fatalf("unreachable finish should not be bundled")
	}
	if _, ok := entries["photos/0003.jpg"]; ok {
		t.Fatalf("url-less finish should not be bundled")
	}

	csvBytes, ok := entries["photo-finishes.csv"]
	if !ok {
		t.Fatalf("csv missing; entries: %v", keys(entries))
	}
	csvBytes = bytes.TrimPrefix(csvBytes, []byte("\xEF\xBB\xBF"))
	rows, err := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 1+len(finishes) {
		t.Fatalf("csv rows = %d, want %d (header + finishes)", len(rows), 1+len(finishes))
	}
	header := rows[0]
	photoCol := indexOf(header, "photo")
	if photoCol < 0 {
		t.Fatalf("csv has no `photo` column: %v", header)
	}
	// Newest first: row 1 is finish "a" (bundled), rows 2/3 have empty photo cells.
	if rows[1][photoCol] != "photos/0001.jpg" {
		t.Fatalf("finish a photo cell = %q, want photos/0001.jpg", rows[1][photoCol])
	}
	if rows[2][photoCol] != "" || rows[3][photoCol] != "" {
		t.Fatalf("unreachable/url-less finishes should have empty photo cells: %q %q", rows[2][photoCol], rows[3][photoCol])
	}
}

func TestZipFinishes_noFetcherStillExportsRows(t *testing.T) {
	finishes := []MergedFinish{
		{Photo: sqlite.Photo{ID: "a", TimeMs: 2000, Bib: "5", CameraLabel: "cam1", BestPhotoURL: "http://phone-a/best.jpg"}, Cams: []string{"cam1"}, MergedCount: 1},
	}
	fetch := func(string) ([]byte, error) { return nil, fmt.Errorf("нет кэша фото") }

	data, _, err := zipFinishes(finishes, fetch)
	if err != nil {
		t.Fatalf("zipFinishes: %v", err)
	}
	entries := readZip(t, data)
	if len(entries) != 1 {
		t.Fatalf("expected only the csv, got %v", keys(entries))
	}
	csvBytes := bytes.TrimPrefix(entries["photo-finishes.csv"], []byte("\xEF\xBB\xBF"))
	rows, _ := csv.NewReader(bytes.NewReader(csvBytes)).ReadAll()
	if len(rows) != 2 {
		t.Fatalf("csv rows = %d, want 2 (header + 1 finish)", len(rows))
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func indexOf(row []string, col string) int {
	for i, c := range row {
		if c == col {
			return i
		}
	}
	return -1
}
