package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// PhotoMetadataVersion is comparable but does not expose a mutable DB handle.
// Database identity prevents reuse after reopening another event file.
type PhotoMetadataVersion struct {
	database *sql.DB
	Revision int64
}

func (s *Store) PhotoMetadataVersion(ctx context.Context, eventID string) (PhotoMetadataVersion, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM photo_metadata_revisions WHERE event_id=?),0)`, eventID).Scan(&revision)
	if err != nil {
		return PhotoMetadataVersion{}, fmt.Errorf("photo metadata version: %w", err)
	}
	return PhotoMetadataVersion{database: s.root, Revision: revision}, nil
}
