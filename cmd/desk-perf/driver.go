package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	sqlite "modernc.org/sqlite"
	"sync/atomic"
	"time"
)

// The diagnostic build overlay selects this driver. It delegates unchanged SQL
// to the pinned driver; timings exclude database/sql's connection-pool wait.
var sqlQueries, sqlExecs, sqlPrepares, sqlBegins, sqlCommits, sqlRollbacks, sqlRows, sqlNanos atomic.Int64

type meteredDriver struct{ inner driver.Driver }

func (d meteredDriver) Open(name string) (driver.Conn, error) {
	c, e := d.inner.Open(name)
	if e != nil {
		return nil, e
	}
	return meteredConn{c}, nil
}

type meteredConn struct{ driver.Conn }

func measure(start time.Time) { sqlNanos.Add(time.Since(start).Nanoseconds()) }
func (c meteredConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	sqlExecs.Add(1)
	s := time.Now()
	defer measure(s)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, a)
}
func (c meteredConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	sqlQueries.Add(1)
	s := time.Now()
	defer measure(s)
	r, e := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, a)
	if e != nil {
		return nil, e
	}
	return meteredRows{r}, nil
}
func (c meteredConn) Prepare(q string) (driver.Stmt, error) {
	sqlPrepares.Add(1)
	s := time.Now()
	defer measure(s)
	p, e := c.Conn.Prepare(q)
	if e != nil {
		return nil, e
	}
	return meteredStmt{p}, nil
}
func (c meteredConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	sqlPrepares.Add(1)
	s := time.Now()
	defer measure(s)
	p, e := c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
	if e != nil {
		return nil, e
	}
	return meteredStmt{p}, nil
}
func (c meteredConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c meteredConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	sqlBegins.Add(1)
	s := time.Now()
	defer measure(s)
	tx, e := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	return meteredTx{tx}, nil
}
func (c meteredConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

type meteredStmt struct{ driver.Stmt }

func (s meteredStmt) Exec(v []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(v))
}
func (s meteredStmt) ExecContext(ctx context.Context, v []driver.NamedValue) (driver.Result, error) {
	sqlExecs.Add(1)
	start := time.Now()
	defer measure(start)
	return s.Stmt.(driver.StmtExecContext).ExecContext(ctx, v)
}
func (s meteredStmt) Query(v []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(v))
}
func (s meteredStmt) QueryContext(ctx context.Context, v []driver.NamedValue) (driver.Rows, error) {
	sqlQueries.Add(1)
	start := time.Now()
	defer measure(start)
	r, e := s.Stmt.(driver.StmtQueryContext).QueryContext(ctx, v)
	if e != nil {
		return nil, e
	}
	return meteredRows{r}, nil
}

func namedValues(v []driver.Value) []driver.NamedValue {
	args := make([]driver.NamedValue, len(v))
	for i := range v {
		args[i] = driver.NamedValue{Ordinal: i + 1, Value: v[i]}
	}
	return args
}

type meteredRows struct{ driver.Rows }

func (r meteredRows) Next(v []driver.Value) error {
	s := time.Now()
	defer measure(s)
	e := r.Rows.Next(v)
	if e == nil {
		sqlRows.Add(1)
	}
	return e
}
func (r meteredRows) Close() error { s := time.Now(); defer measure(s); return r.Rows.Close() }

type meteredTx struct{ driver.Tx }

func (t meteredTx) Commit() error {
	sqlCommits.Add(1)
	s := time.Now()
	defer measure(s)
	return t.Tx.Commit()
}
func (t meteredTx) Rollback() error {
	sqlRollbacks.Add(1)
	s := time.Now()
	defer measure(s)
	return t.Tx.Rollback()
}
func init() { sql.Register("deskperf_sqlite", meteredDriver{&sqlite.Driver{}}) }

type sqlSample struct {
	Queries   int64   `json:"queries"`
	Execs     int64   `json:"execs"`
	Prepares  int64   `json:"prepares"`
	Begins    int64   `json:"begins"`
	Commits   int64   `json:"commits"`
	Rollbacks int64   `json:"rollbacks"`
	Rows      int64   `json:"rows"`
	DriverMS  float64 `json:"driver_ms"`
}

func sqlSnapshot() sqlSample {
	return sqlSample{sqlQueries.Load(), sqlExecs.Load(), sqlPrepares.Load(), sqlBegins.Load(), sqlCommits.Load(), sqlRollbacks.Load(), sqlRows.Load(), float64(sqlNanos.Load()) / 1e6}
}
func (s sqlSample) since(a sqlSample) sqlSample {
	return sqlSample{s.Queries - a.Queries, s.Execs - a.Execs, s.Prepares - a.Prepares, s.Begins - a.Begins, s.Commits - a.Commits, s.Rollbacks - a.Rollbacks, s.Rows - a.Rows, s.DriverMS - a.DriverMS}
}
