// Package store owns the SQLite database: opening it with the right
// per-connection pragmas, running embedded migrations, and the typed queries
// the rest of the system uses. Nothing outside this package writes SQL.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Store is a handle to the database. It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string
}

const (
	// maxOpenConns bounds the pool. WAL lets readers run alongside one writer;
	// writers serialize on busy_timeout. 1.x needed >1 because it iterated a
	// cursor while issuing further statements; the same holds here.
	maxOpenConns = 8
	busyTimeout  = 5 * time.Second
)

// Open opens (creating if needed) the database at path and applies the
// connection pragmas. It does not run migrations; call Migrate.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	// The database holds session ids, encrypted credentials and a few plain
	// settings (download proxy, GitHub token): never world-readable, whatever
	// umask created it under.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			_ = db.Close()
			return nil, fmt.Errorf("chmod %s: %w", path+suffix, err)
		}
	}
	return &Store{db: db, path: path}, nil
}

// OpenMemory opens a private in-memory database. Each call is independent.
// It is for tests; production always uses a file.
func OpenMemory(ctx context.Context) (*Store, error) {
	// A shared-cache name is required so every pooled connection sees the same
	// in-memory database instead of its own empty one.
	name := fmt.Sprintf("file:ergw-mem-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := sql.Open("sqlite", name+"&"+pragmaQuery())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, path: ":memory:"}, nil
}

// dsn builds the modernc DSN. Pragmas passed via _pragma apply to every
// connection the pool opens, which is the whole point: PRAGMA statements run
// by hand only affect the one connection that ran them.
func dsn(path string) string {
	return "file:" + path + "?" + pragmaQuery()
}

func pragmaQuery() string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_time_format", "sqlite")
	return q.Encode()
}

// DB exposes the underlying pool for packages that need transactions.
func (s *Store) DB() *sql.DB { return s.db }

// Path is the database file, or ":memory:".
func (s *Store) Path() string { return s.path }

// Close closes the pool.
func (s *Store) Close() error { return s.db.Close() }

// WithTx runs fn inside a transaction, committing on nil and rolling back on
// error or panic.
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	return fn(tx)
}
