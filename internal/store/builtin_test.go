package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestBuiltinRuleSetCreatedAndSeeded(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	sets, _ := st.ListRuleSets(ctx)
	if len(sets) != 1 || !sets[0].Builtin || sets[0].Name != BuiltinRuleSetName || sets[0].Position != 0 {
		t.Fatalf("sets %+v", sets)
	}
	rules, _ := st.ListRules(ctx, sets[0].ID)
	if len(rules) != len(BuiltinRules) || rules[0].MatchValue != "private" || rules[1].MatchValue != "cn" || rules[1].ActionKind != model.ActionDirect {
		t.Fatalf("rules %+v", rules)
	}
	// Deleting a seeded rule sticks across restarts (seeding happens once).
	_, _ = st.Mutate(ctx, Mutation{Actor: "t"}, func(tx *sql.Tx) error { return DeleteRule(ctx, tx, rules[1].ID) })
	if err := st.EnsureBuiltinRuleSet(ctx); err != nil {
		t.Fatal(err)
	}
	if rules, _ = st.ListRules(ctx, sets[0].ID); len(rules) != len(BuiltinRules)-1 {
		t.Fatalf("deleted built-in rule came back: %+v", rules)
	}
	// It cannot be deleted or moved off the first position.
	err := st.WithTx(ctx, func(tx *sql.Tx) error { return DeleteRuleSet(ctx, tx, sets[0].ID) })
	if err != ErrBuiltinRuleSet {
		t.Fatalf("delete: %v", err)
	}
	other := uuid.NewString()
	_, _ = st.Mutate(ctx, Mutation{Actor: "t"}, func(tx *sql.Tx) error { return InsertRuleSet(ctx, tx, other, "mine") })
	err = st.WithTx(ctx, func(tx *sql.Tx) error { return ReorderRuleSets(ctx, tx, []string{other, sets[0].ID}) })
	if err != ErrBuiltinRuleSet {
		t.Fatalf("reorder: %v", err)
	}
	if err := st.WithTx(ctx, func(tx *sql.Tx) error { return ReorderRuleSets(ctx, tx, []string{sets[0].ID, other}) }); err != nil {
		t.Fatalf("valid reorder: %v", err)
	}
}

func TestBuiltinRuleSetAdoptsImportedDefault(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Simulate a 1.x import done on a build without the built-in set: run
	// only 0001, insert sets, then let the rest of the migrations adopt.
	all, _ := Migrations()
	if _, err := st.db.ExecContext(ctx, all[0].SQL); err != nil {
		t.Fatal(err)
	}
	_, _ = st.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT '')`)
	_, _ = st.db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, all[0].Version)
	_, _ = st.db.ExecContext(ctx, `INSERT INTO rule_sets (id,name,position,enabled) VALUES ('s-streams','流媒体',0,1),('s-default','默认',1,1)`)
	_, _ = st.db.ExecContext(ctx, `INSERT INTO rules (id,set_id,position,match_kind,match_value,action_kind,enabled) VALUES ('r1','s-default',0,'domain','www.google.com','proxy',1)`)
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	sets, _ := st.ListRuleSets(ctx)
	if len(sets) != 2 || sets[0].ID != "s-default" || !sets[0].Builtin || sets[0].Position != 0 || sets[1].ID != "s-streams" {
		t.Fatalf("adopted order %+v", sets)
	}
	rules, _ := st.ListRules(ctx, "s-default")
	if len(rules) != 1+len(BuiltinRules) || rules[0].MatchValue != "www.google.com" {
		t.Fatalf("imported rule must keep precedence over seeded ones: %+v", rules)
	}
}
