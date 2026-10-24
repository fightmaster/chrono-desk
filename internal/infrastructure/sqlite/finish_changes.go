package sqlite

import "gitlab.com/fightmaster1/chrono-desk/internal/finishchange"

// FinishChanges is the store-lifetime, single-dispatcher projection signal.
func (s *Store) FinishChanges() *finishchange.Signal { return s.finishChanges }

// finishChanged collects nested writes until the outermost transaction commits.
// Standalone statements call it only after successful autocommit.
func (s *Store) finishChanged() {
	if s.finishDirty != nil {
		*s.finishDirty = true
	} else if s.finishChanges != nil {
		s.finishChanges.Notify()
	}
}

func (s *Store) committedFinishes() {
	if s.finishDirty != nil && *s.finishDirty && s.finishChanges != nil {
		s.finishChanges.Notify()
	}
}
