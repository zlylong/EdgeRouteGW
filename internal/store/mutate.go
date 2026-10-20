package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// Mutation describes a desired-state change for the changeset ledger.
type Mutation struct {
	Actor   string
	Summary string
	Diff    any // marshalled to JSON; nil becomes {}
}

// Mutate runs fn in a transaction, bumps the generation and records a
// changeset. Every API write that changes what the reconciler should apply
// goes through here so a change is either fully visible to the reconciler or
// not at all. It returns the new generation.
func (s *Store) Mutate(ctx context.Context, m Mutation, fn func(tx *sql.Tx) error) (int64, error) {
	var gen int64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		g, err := BumpGeneration(ctx, tx)
		if err != nil {
			return err
		}
		gen = g
		diff := "{}"
		if m.Diff != nil {
			b, err := json.Marshal(m.Diff)
			if err != nil {
				return fmt.Errorf("encode changeset diff: %w", err)
			}
			diff = string(b)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO changesets (id, generation, actor, summary, diff) VALUES (?,?,?,?,?)`,
			uuid.NewString(), g, m.Actor, m.Summary, diff)
		return err
	})
	return gen, err
}

// ListChangesets returns the most recent changesets, newest first.
func (s *Store) ListChangesets(ctx context.Context, limit int) ([]model.Changeset, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, generation, actor, summary, diff, COALESCE(reverted_by,''), created_at
		FROM changesets ORDER BY generation DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Changeset
	for rows.Next() {
		var c model.Changeset
		var created string
		if err := rows.Scan(&c.ID, &c.Generation, &c.Actor, &c.Summary, &c.Diff, &c.RevertedBy, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTime(created)
		out = append(out, c)
	}
	if out == nil {
		out = []model.Changeset{}
	}
	return out, rows.Err()
}

// RecordApply appends one driver action to apply_log.
func (s *Store) RecordApply(ctx context.Context, e model.ApplyLogEntry) error {
	ok := 0
	if e.OK {
		ok = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO apply_log (generation, component, action, ok, duration_ms, error) VALUES (?,?,?,?,?,?)`,
		e.Generation, string(e.Component), e.Action, ok, e.DurationMs, e.Error)
	return err
}

// ListApplyLog returns recent apply entries, newest first, optionally for one
// generation (0 = all).
func (s *Store) ListApplyLog(ctx context.Context, generation int64, limit int) ([]model.ApplyLogEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT id, generation, component, action, ok, duration_ms, error, ts FROM apply_log`
	args := []any{}
	if generation > 0 {
		q += ` WHERE generation = ?`
		args = append(args, generation)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ApplyLogEntry{}
	for rows.Next() {
		var e model.ApplyLogEntry
		var comp, ts string
		var ok int
		if err := rows.Scan(&e.ID, &e.Generation, &comp, &e.Action, &ok, &e.DurationMs, &e.Error, &ts); err != nil {
			return nil, err
		}
		e.Component = model.Component(comp)
		e.OK = ok != 0
		e.At = parseTime(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddEvent persists a structured event.
func (s *Store) AddEvent(ctx context.Context, e model.Event) error {
	details := e.Details
	if details == "" {
		details = "{}"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (level, module, type, message, trace_id, actor, details) VALUES (?,?,?,?,?,?,?)`,
		string(e.Level), e.Module, e.Type, e.Message, e.TraceID, e.Actor, details)
	return err
}

// EventFilter narrows ListEvents.
type EventFilter struct {
	Module   string
	Level    model.EventLevel
	BeforeID int64
	Limit    int
}

// ListEvents returns events newest first.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]model.Event, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	q := `SELECT id, ts, level, module, type, message, trace_id, actor, details FROM events WHERE 1=1`
	args := []any{}
	if f.Module != "" {
		q += ` AND module = ?`
		args = append(args, f.Module)
	}
	if f.Level != "" {
		q += ` AND level = ?`
		args = append(args, string(f.Level))
	}
	if f.BeforeID > 0 {
		q += ` AND id < ?`
		args = append(args, f.BeforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		var ts, lvl string
		if err := rows.Scan(&e.ID, &ts, &lvl, &e.Module, &e.Type, &e.Message, &e.TraceID, &e.Actor, &e.Details); err != nil {
			return nil, err
		}
		e.At = parseTime(ts)
		e.Level = model.EventLevel(lvl)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrNotFound is returned when a row addressed by id does not exist.
var ErrNotFound = sql.ErrNoRows

func affectedOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
