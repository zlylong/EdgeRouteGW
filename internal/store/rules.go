package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// ListRuleSets returns rule sets in display order.
func (s *Store) ListRuleSets(ctx context.Context) ([]model.RuleSet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, position, enabled, builtin, created_at, updated_at FROM rule_sets ORDER BY builtin DESC, position, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.RuleSet{}
	for rows.Next() {
		var r model.RuleSet
		var enabled, builtin int
		var created, updated string
		if err := rows.Scan(&r.ID, &r.Name, &r.Position, &enabled, &builtin, &created, &updated); err != nil {
			return nil, err
		}
		r.Enabled, r.Builtin = enabled != 0, builtin != 0
		r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRules returns rules ordered by set position then rule position. setID
// "" means all sets.
func (s *Store) ListRules(ctx context.Context, setID string) ([]model.Rule, error) {
	q := `SELECT r.id, r.set_id, r.position, r.match_kind, r.match_value, r.action_kind, COALESCE(r.action_ref,''), r.enabled, r.remark, r.created_at, r.updated_at
		FROM rules r JOIN rule_sets s ON s.id = r.set_id`
	args := []any{}
	if setID != "" {
		q += ` WHERE r.set_id = ?`
		args = append(args, setID)
	}
	q += ` ORDER BY s.position, s.created_at, r.position, r.created_at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Rule{}
	for rows.Next() {
		var r model.Rule
		var mk, ak, created, updated string
		var enabled int
		if err := rows.Scan(&r.ID, &r.SetID, &r.Position, &mk, &r.MatchValue, &ak, &r.ActionRef, &enabled, &r.Remark, &created, &updated); err != nil {
			return nil, err
		}
		r.MatchKind, r.ActionKind = model.MatchKind(mk), model.ActionKind(ak)
		r.Enabled = enabled != 0
		r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRuleSet appends a set at the end.
func InsertRuleSet(ctx context.Context, tx *sql.Tx, id, name string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO rule_sets (id, name, position, enabled)
		VALUES (?, ?, COALESCE((SELECT MAX(position)+1 FROM rule_sets), 0), 1)`, id, name)
	return err
}

// UpdateRuleSet renames / toggles a set.
func UpdateRuleSet(ctx context.Context, tx *sql.Tx, id, name string, enabled bool) error {
	return affectedOne(tx.ExecContext(ctx, `UPDATE rule_sets SET name=?, enabled=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, name, b2i(enabled), id))
}

// ErrBuiltinRuleSet is returned for operations the built-in set refuses.
var ErrBuiltinRuleSet = fmt.Errorf("the built-in default rule set cannot be deleted or moved")

// DeleteRuleSet removes a set and (by cascade) its rules.
func DeleteRuleSet(ctx context.Context, tx *sql.Tx, id string) error {
	var builtin int
	if err := tx.QueryRowContext(ctx, `SELECT builtin FROM rule_sets WHERE id=?`, id).Scan(&builtin); err == nil && builtin == 1 {
		return ErrBuiltinRuleSet
	}
	return affectedOne(tx.ExecContext(ctx, `DELETE FROM rule_sets WHERE id=?`, id))
}

// ReorderRuleSets assigns positions in the order given. Every existing set
// must appear exactly once.
func ReorderRuleSets(ctx context.Context, tx *sql.Tx, ids []string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rule_sets`).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return fmt.Errorf("reorder must list all %d rule sets, got %d", n, len(ids))
	}
	var builtinID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM rule_sets WHERE builtin=1`).Scan(&builtinID); err == nil && len(ids) > 0 && ids[0] != builtinID {
		return ErrBuiltinRuleSet
	}
	for i, id := range ids {
		if err := affectedOne(tx.ExecContext(ctx, `UPDATE rule_sets SET position=? WHERE id=?`, i, id)); err != nil {
			return fmt.Errorf("rule set %s: %w", id, err)
		}
	}
	return nil
}

// InsertRule appends a rule to its set.
func InsertRule(ctx context.Context, tx *sql.Tx, r model.Rule) error {
	var ref any
	if r.ActionRef != "" {
		ref = r.ActionRef
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO rules (id, set_id, position, match_kind, match_value, action_kind, action_ref, enabled, remark)
		VALUES (?, ?, COALESCE((SELECT MAX(position)+1 FROM rules WHERE set_id = ?), 0), ?, ?, ?, ?, ?, ?)`,
		r.ID, r.SetID, r.SetID, string(r.MatchKind), r.MatchValue, string(r.ActionKind), ref, b2i(r.Enabled), r.Remark)
	return err
}

// UpdateRule rewrites a rule's editable fields (not its set or position).
func UpdateRule(ctx context.Context, tx *sql.Tx, r model.Rule) error {
	var ref any
	if r.ActionRef != "" {
		ref = r.ActionRef
	}
	return affectedOne(tx.ExecContext(ctx, `UPDATE rules SET match_kind=?, match_value=?, action_kind=?, action_ref=?, enabled=?, remark=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		string(r.MatchKind), r.MatchValue, string(r.ActionKind), ref, b2i(r.Enabled), r.Remark, r.ID))
}

// DeleteRule removes one rule.
func DeleteRule(ctx context.Context, tx *sql.Tx, id string) error {
	return affectedOne(tx.ExecContext(ctx, `DELETE FROM rules WHERE id=?`, id))
}

// ReorderRules sets positions within one set; ids must be exactly that set's
// rules.
func ReorderRules(ctx context.Context, tx *sql.Tx, setID string, ids []string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rules WHERE set_id=?`, setID).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return fmt.Errorf("reorder must list all %d rules of the set, got %d", n, len(ids))
	}
	for i, id := range ids {
		if err := affectedOne(tx.ExecContext(ctx, `UPDATE rules SET position=? WHERE id=? AND set_id=?`, i, id, setID)); err != nil {
			return fmt.Errorf("rule %s: %w", id, err)
		}
	}
	return nil
}

// MoveRule relocates a rule to another set at the end.
func MoveRule(ctx context.Context, tx *sql.Tx, id, toSet string) error {
	return affectedOne(tx.ExecContext(ctx, `UPDATE rules SET set_id=?, position=COALESCE((SELECT MAX(position)+1 FROM rules WHERE set_id=?),0),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, toSet, toSet, id))
}
