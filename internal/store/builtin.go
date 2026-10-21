package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// BuiltinRuleSetName is the display name of the pinned default rule set.
const BuiltinRuleSetName = "默认"

// builtinRulesSeededKey marks that the default direct rules were added once;
// an operator who deletes them is not nagged on every start.
const builtinRulesSeededKey = "builtin_rules.seeded"

// BuiltinRules are the rules every gateway starts with. They sit in the
// built-in set and may be edited or disabled; LAN/private traffic and
// domestic destinations go direct unless the operator decides otherwise.
var BuiltinRules = []model.Rule{
	{MatchKind: model.MatchGeoIP, MatchValue: "private", ActionKind: model.ActionDirect, Remark: "私有网络直连"},
	{MatchKind: model.MatchGeosite, MatchValue: "cn", ActionKind: model.ActionDirect, Remark: "国内域名直连"},
	{MatchKind: model.MatchGeoIP, MatchValue: "cn", ActionKind: model.ActionDirect, Remark: "国内 IP 直连"},
}

// BuiltinRuleSet returns the id of the built-in set ("" if none yet).
func (s *Store) BuiltinRuleSet(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM rule_sets WHERE builtin=1 ORDER BY position LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// EnsureBuiltinRuleSet runs after migrations: adopts an existing "默认" set
// (1.x imports created one) or creates the built-in set, pins it first and
// seeds the default rules once. Idempotent. It is schema-level seeding and
// does not bump the generation: the reconciler's first pass after start
// renders the whole snapshot anyway.
func (s *Store) EnsureBuiltinRuleSet(ctx context.Context) error {
	id, err := s.BuiltinRuleSet(ctx)
	if err != nil {
		return err
	}
	seeded, _ := s.GetSetting(ctx, builtinRulesSeededKey)
	if id != "" && seeded == "1" {
		return s.pinBuiltinFirst(ctx)
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if id == "" {
			// Adopt the first set called 默认 (importer output) if there is one.
			err := tx.QueryRowContext(ctx, `SELECT id FROM rule_sets WHERE name=? ORDER BY position, created_at LIMIT 1`, BuiltinRuleSetName).Scan(&id)
			switch {
			case err == sql.ErrNoRows:
				id = uuid.NewString()
				if err := InsertBuiltinRuleSet(ctx, tx, id); err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if _, err := tx.ExecContext(ctx, `UPDATE rule_sets SET builtin=1 WHERE id=?`, id); err != nil {
					return err
				}
			}
		}
		if seeded != "1" {
			if err := SeedBuiltinRules(ctx, tx, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, '1') ON CONFLICT(key) DO UPDATE SET value='1'`, builtinRulesSeededKey); err != nil {
				return err
			}
		}
		return renumberRuleSets(ctx, tx)
	})
}

// InsertBuiltinRuleSet creates the pinned set (used by the importer after
// --replace wiped the tables).
func InsertBuiltinRuleSet(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO rule_sets (id, name, position, enabled, builtin) VALUES (?, ?, -1, 1, 1)`, id, BuiltinRuleSetName)
	return err
}

// SeedBuiltinRules appends the default rules that the set does not have yet
// (after any rules the operator or the importer put there, so specific
// rules keep precedence).
func SeedBuiltinRules(ctx context.Context, tx *sql.Tx, setID string) error {
	for _, r := range BuiltinRules {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rules WHERE set_id=? AND match_kind=? AND match_value=?`, setID, string(r.MatchKind), r.MatchValue).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		r.ID, r.SetID, r.Enabled = uuid.NewString(), setID, true
		if err := InsertRule(ctx, tx, r); err != nil {
			return fmt.Errorf("seed rule %s:%s: %w", r.MatchKind, r.MatchValue, err)
		}
	}
	return nil
}

// renumberRuleSets gives sets consecutive positions with the built-in one first.
func renumberRuleSets(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM rule_sets ORDER BY builtin DESC, position, created_at`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE rule_sets SET position=? WHERE id=?`, i, id); err != nil {
			return err
		}
	}
	return nil
}

// pinBuiltinFirst fixes the order (cheap no-op when already first).
func (s *Store) pinBuiltinFirst(ctx context.Context) error {
	var pos int
	if err := s.db.QueryRowContext(ctx, `SELECT position FROM rule_sets WHERE builtin=1`).Scan(&pos); err != nil {
		return err
	}
	var minPos int
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(position) FROM rule_sets`).Scan(&minPos); err != nil {
		return err
	}
	if pos == minPos {
		return nil
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error { return renumberRuleSets(ctx, tx) })
}

// HasUserData reports whether the store holds nodes or rules beyond the
// seeded built-in ones (the importer refuses to run on such a store
// without --replace).
func (s *Store) HasUserData(ctx context.Context) (bool, error) {
	var nodes int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&nodes); err != nil {
		return false, err
	}
	if nodes > 0 {
		return true, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.match_kind, r.match_value, s.builtin FROM rules r JOIN rule_sets s ON s.id = r.set_id`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, value string
		var builtin int
		if err := rows.Scan(&kind, &value, &builtin); err != nil {
			return false, err
		}
		seeded := false
		if builtin == 1 {
			for _, b := range BuiltinRules {
				if string(b.MatchKind) == kind && b.MatchValue == value {
					seeded = true
					break
				}
			}
		}
		if !seeded {
			return true, nil
		}
	}
	return false, rows.Err()
}
