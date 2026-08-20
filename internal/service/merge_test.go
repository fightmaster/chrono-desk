package service

import "testing"
import "gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"

func p(source, cam string, t int64, bib, bibSrc string) sqlite.Photo {
	return sqlite.Photo{
		ID: source + ":" + itoa(t), SourceID: source, CameraLabel: cam,
		TimeMs: t, Bib: bib, BibSource: bibSrc,
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestMergeDifferentCamerasSameCrossing(t *testing.T) {
	got := MergeFinishes([]sqlite.Photo{
		p("A", "Cam A", 1000, "", "none"),
		p("B", "Cam B", 1200, "", "none"),
	}, MergeWindowMs)
	if len(got) != 1 {
		t.Fatalf("want 1 merged finish, got %d", len(got))
	}
	if got[0].MergedCount != 2 || len(got[0].Cams) != 2 {
		t.Fatalf("want 2 cameras merged, got count=%d cams=%v", got[0].MergedCount, got[0].Cams)
	}
}

func TestSameCameraNeverMerges(t *testing.T) {
	// One camera sees a crossing once, so two rows from it are two runners.
	got := MergeFinishes([]sqlite.Photo{
		p("A", "Cam A", 1000, "", "none"),
		p("A", "Cam A", 1150, "", "none"),
	}, MergeWindowMs)
	if len(got) != 2 {
		t.Fatalf("want 2 finishes (same camera not merged), got %d", len(got))
	}
}

func TestConflictingBibsDoNotMerge(t *testing.T) {
	got := MergeFinishes([]sqlite.Photo{
		p("A", "Cam A", 1000, "247", "manual"),
		p("B", "Cam B", 1100, "301", "manual"),
	}, MergeWindowMs)
	if len(got) != 2 {
		t.Fatalf("want 2 finishes (different bibs), got %d", len(got))
	}
}

func TestBeyondWindowDoesNotMerge(t *testing.T) {
	got := MergeFinishes([]sqlite.Photo{
		p("A", "Cam A", 1000, "", "none"),
		p("B", "Cam B", 1000+MergeWindowMs+1, "", "none"),
	}, MergeWindowMs)
	if len(got) != 2 {
		t.Fatalf("want 2 finishes (outside window), got %d", len(got))
	}
}

func TestManualBibIsRepresentativeAndNewestFirst(t *testing.T) {
	got := MergeFinishes([]sqlite.Photo{
		p("A", "Cam A", 5000, "", "none"),      // newest, no bib
		p("B", "Cam B", 4900, "247", "manual"), // older but confirmed
		p("A", "Cam A", 1000, "", "none"),      // a separate, earlier crossing
	}, MergeWindowMs)
	if len(got) != 2 {
		t.Fatalf("want 2 finishes, got %d", len(got))
	}
	// Newest crossing first.
	if got[0].TimeMs < got[1].TimeMs {
		t.Fatalf("expected newest-first ordering, got %d then %d", got[0].TimeMs, got[1].TimeMs)
	}
	// The merged newest finish should adopt the manually-confirmed bib.
	if got[0].Bib != "247" || got[0].BibSource != "manual" {
		t.Fatalf("want manual bib 247 as representative, got %q/%q", got[0].Bib, got[0].BibSource)
	}
}
