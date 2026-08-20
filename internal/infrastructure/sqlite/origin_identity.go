package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

const originStateFile = ".observation-origin.sqlite"

// installationOrigin owns the monotonic journal sequence shared by every
// event database of one Chrono Desk installation. A separate SQLite file makes
// allocation safe across restarts and two accidentally concurrent app
// processes. Sequence allocation precedes the event transaction: gaps are
// valid after rollback, but a committed sequence is never reused.
type installationOrigin struct {
	db         *sql.DB
	instanceID string
}

func loadOrCreateInstallationOrigin(dataDir string) (*installationOrigin, error) {
	db, err := Open(filepath.Join(dataDir, originStateFile))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS observation_origin (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			instance_id TEXT NOT NULL,
			last_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_sequence >= 0)
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create observation origin state: %w", err)
	}
	id, err := newUUID()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("generate observation origin id: %w", err)
	}
	if _, err := db.Exec(`
		INSERT OR IGNORE INTO observation_origin (singleton, instance_id, last_sequence)
		VALUES (1, ?, 0)`, id); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize observation origin state: %w", err)
	}
	if err := db.QueryRow(`SELECT instance_id FROM observation_origin WHERE singleton = 1`).Scan(&id); err != nil {
		db.Close()
		return nil, fmt.Errorf("read observation origin state: %w", err)
	}
	if id == "" {
		db.Close()
		return nil, fmt.Errorf("invalid observation origin state")
	}
	return &installationOrigin{db: db, instanceID: id}, nil
}

func (o *installationOrigin) Next() (int64, error) {
	var sequence int64
	err := o.db.QueryRowContext(context.Background(), `
		UPDATE observation_origin
		SET last_sequence = last_sequence + 1
		WHERE singleton = 1 AND last_sequence < 9223372036854775807
		RETURNING last_sequence`).Scan(&sequence)
	if err != nil {
		return 0, fmt.Errorf("allocate observation origin sequence: %w", err)
	}
	return sequence, nil
}

func (o *installationOrigin) Close() error {
	return o.db.Close()
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16])), nil
}
