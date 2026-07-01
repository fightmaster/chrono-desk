package service

import (
	"sort"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// MergeWindowMs is how close in time two photos from DIFFERENT cameras must be to be
// treated as the same finish crossing. Kept modest because the desk now aligns every
// phone's clock to itself, so genuine duplicates land within a few hundred ms.
const MergeWindowMs = 1200

// MergedFinish is one crossing after collapsing the copies caught by several cameras.
// It embeds the representative photo (so it carries the same fields the wall already
// renders) plus the list of contributing cameras.
type MergedFinish struct {
	sqlite.Photo
	Cams        []string `json:"cams"`
	MergedCount int      `json:"merged_count"`
}

// MergeFinishes collapses the same crossing seen by multiple cameras into one finish.
//
// Rules:
//   - Photos from the SAME camera are never merged — one camera sees a crossing once,
//     so two rows from it are two different runners.
//   - Photos from DIFFERENT cameras merge when within windowMs of the cluster's anchor
//     (earliest) time and their bibs don't conflict: a matching or empty bib is fine,
//     two differing non-empty bibs block the merge.
//
// Input is one event's photos (any order); output is newest crossing first. Pure —
// no DB, no clock — so it is unit-tested directly.
func MergeFinishes(photos []sqlite.Photo, windowMs int64) []MergedFinish {
	if windowMs <= 0 {
		windowMs = MergeWindowMs
	}
	asc := make([]sqlite.Photo, len(photos))
	copy(asc, photos)
	sort.SliceStable(asc, func(i, j int) bool { return asc[i].TimeMs < asc[j].TimeMs })

	type cluster struct {
		items  []sqlite.Photo
		cams   map[string]bool // keyed by device (source_id), not the display label
		anchor int64
		bib    string
	}
	var clusters []*cluster

	deviceKey := func(p sqlite.Photo) string {
		if p.SourceID != "" {
			return p.SourceID
		}
		return p.CameraLabel
	}

	for _, p := range asc {
		dev := deviceKey(p)
		var target *cluster
		// Only clusters whose anchor is within the window can accept p; scan newest
		// first and stop once we pass the window (asc order ⇒ older clusters are worse).
		for i := len(clusters) - 1; i >= 0; i-- {
			c := clusters[i]
			if p.TimeMs-c.anchor > windowMs {
				break
			}
			if c.cams[dev] {
				continue // this camera already contributed to that cluster
			}
			if c.bib != "" && p.Bib != "" && c.bib != p.Bib {
				continue // conflicting numbers ⇒ different runners
			}
			target = c
			break
		}
		if target == nil {
			target = &cluster{anchor: p.TimeMs, cams: map[string]bool{}, bib: p.Bib}
			clusters = append(clusters, target)
		}
		target.items = append(target.items, p)
		target.cams[dev] = true
		if target.bib == "" {
			target.bib = p.Bib
		}
	}

	out := make([]MergedFinish, 0, len(clusters))
	for _, c := range clusters {
		out = append(out, mergeCluster(c.items))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeMs > out[j].TimeMs })
	return out
}

// mergeCluster picks a representative for a cluster (a manually-confirmed bib wins,
// else the newest photo) and gathers the distinct camera labels.
func mergeCluster(items []sqlite.Photo) MergedFinish {
	rep := items[0]
	for _, p := range items {
		if p.TimeMs > rep.TimeMs {
			rep = p
		}
	}
	for _, p := range items {
		if p.BibSource == "manual" && p.Bib != "" {
			rep = p
			break
		}
	}
	seen := map[string]bool{}
	cams := make([]string, 0, len(items))
	for _, p := range items {
		if p.CameraLabel != "" && !seen[p.CameraLabel] {
			seen[p.CameraLabel] = true
			cams = append(cams, p.CameraLabel)
		}
	}
	return MergedFinish{Photo: rep, Cams: cams, MergedCount: len(items)}
}
