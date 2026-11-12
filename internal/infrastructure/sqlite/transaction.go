package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
)

// ownedTransaction reserves the connection until its outcome is settled.
// database/sql marks a Tx done even when SQLite rejects COMMIT (for example,
// a deferred foreign key), so Tx.Rollback cannot repair that connection.
type ownedTransaction struct {
	*sql.Tx
	conn *sql.Conn
}

func beginTransaction(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*ownedTransaction, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &ownedTransaction{Tx: tx, conn: conn}, nil
}

func (tx *ownedTransaction) Commit() error {
	err := tx.Tx.Commit()
	tx.release(err)
	return err
}

func (tx *ownedTransaction) Rollback() error {
	err := tx.Tx.Rollback()
	tx.release(err)
	return err
}

func (tx *ownedTransaction) release(outcome error) {
	if outcome != nil {
		// Discard only the reserved connection. Closing it rolls back any
		// remaining SQLite transaction; no SQL runs through the shared pool
		// and no other writer can inherit or have its transaction rolled back.
		// Cancellation may already have discarded it, which is also safe.
		_ = tx.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = tx.conn.Close()
}
