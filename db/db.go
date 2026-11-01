// Package db wraps database/sql with context-carried transactions and per-module migrations.
//
// ekman imports no SQL driver. The application imports one (for example
// github.com/jackc/pgx/v5/stdlib, driver name "pgx") and passes its name to Open. v0.1 targets
// PostgreSQL: migrations use advisory locks and $n placeholders.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Querier is the subset of *sql.DB and *sql.Tx that query code needs. It matches the DBTX
// interface sqlc generates for database/sql, so generated code accepts DB.Q(ctx) directly.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB is a connection pool whose transactions travel in the context.
type DB struct {
	sql *sql.DB
}

// Open opens a pool with a driver the application registered. Like sql.Open it connects
// lazily, so commands that never touch the database (such as "openapi") work without one.
func Open(driverName, dsn string) (*DB, error) {
	pool, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	return &DB{sql: pool}, nil
}

// New wraps an existing pool.
func New(pool *sql.DB) *DB { return &DB{sql: pool} }

// Close closes the pool.
func (d *DB) Close() error { return d.sql.Close() }

// SQL returns the underlying pool.
func (d *DB) SQL() *sql.DB { return d.sql }

// Q returns the transaction carried by ctx if it belongs to d, else the pool.
func (d *DB) Q(ctx context.Context) Querier {
	if st := txFrom(ctx); st != nil && st.db == d {
		return st.tx
	}
	return d.sql
}

type txKey struct{}

type txState struct {
	db    *DB
	tx    *sql.Tx
	after []func(context.Context)
}

func txFrom(ctx context.Context) *txState {
	st, _ := ctx.Value(txKey{}).(*txState)
	return st
}

// Tx runs fn in a transaction carried by the ctx passed to fn. A Tx inside a Tx on the same DB
// joins the outer transaction, so service methods compose without knowing who started it.
// fn's error, or a panic, rolls back; after a commit the AfterCommit hooks run.
func (d *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if st := txFrom(ctx); st != nil && st.db == d {
		return fn(ctx)
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	st := &txState{db: d, tx: tx}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(context.WithValue(ctx, txKey{}, st)); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("db: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	for _, f := range st.after {
		f(ctx)
	}
	return nil
}

// AfterCommit runs fn after the transaction in ctx commits, or immediately when ctx carries
// none. Hooks of a rolled-back transaction never run. fn receives the context outside the
// transaction, so its queries do not touch the committed tx.
func AfterCommit(ctx context.Context, fn func(ctx context.Context)) {
	if st := txFrom(ctx); st != nil {
		st.after = append(st.after, fn)
		return
	}
	fn(ctx)
}
