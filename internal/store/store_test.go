package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

func openMigrated(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	first, err := st.Migrate(ctx)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if len(first) == 0 || first[0] != "0001_init" {
		t.Fatalf("expected 0001_init applied first, got %v", first)
	}
	second, err := st.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second run should apply nothing, got %v", second)
	}
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != first[len(first)-1] {
		t.Fatalf("schema version %q, want %q", v, first[len(first)-1])
	}
}

// TestNodesRebuildKeepsReferences applies the schema up to 0007, creates a
// node that is a group member and a deploy target, then runs the remaining
// migrations: the 0008 table rebuild must keep both references.
func TestNodesRebuildKeepsReferences(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.Version >= "0008" {
			break
		}
		if _, err := s.db.ExecContext(ctx, m.SQL); err != nil {
			t.Fatalf("%s: %v", m.Version, err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, m.Version); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO nodes (id, name, protocol, address, port) VALUES ('n1','n','http',1,1)`)
	exec(`INSERT INTO outbound_groups (id, name, kind) VALUES ('g1','g','failover')`)
	exec(`INSERT INTO outbound_group_members (group_id, node_id, position) VALUES ('g1','n1',0)`)
	exec(`INSERT INTO remote_hosts (id, name, ssh_host, ssh_user, ssh_auth_kind) VALUES ('h1','h','1.2.3.4','root','key')`)
	exec(`INSERT INTO remote_deploys (id, host_id, kind, state, node_id) VALUES ('d1','h1','wg','succeeded','n1')`)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO nodes (id, name, protocol, address, port) VALUES ('n2','hy','hysteria',1,1)`); err == nil {
		t.Fatal("hysteria must be rejected before 0008")
	}
	ran, err := s.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) == 0 || ran[0] != "0008_nodes_hysteria" {
		t.Fatalf("ran %v", ran)
	}
	var members, deployNode int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbound_group_members WHERE node_id='n1'`).Scan(&members); err != nil || members != 1 {
		t.Fatalf("group member lost: %d %v", members, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_deploys WHERE node_id='n1'`).Scan(&deployNode); err != nil || deployNode != 1 {
		t.Fatalf("deploy node_id lost: %d %v", deployNode, err)
	}
	exec(`INSERT INTO nodes (id, name, protocol, address, port) VALUES ('n2','hy','hysteria',1,1)`)
	var fk int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys after migration: %d %v", fk, err)
	}
	// Cascades still work against the rebuilt table.
	exec(`DELETE FROM nodes WHERE id='n1'`)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbound_group_members`).Scan(&members); err != nil || members != 0 {
		t.Fatalf("cascade after rebuild: %d %v", members, err)
	}
}

func TestSchemaVersionBeforeMigrate(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion on empty db: %v", err)
	}
	if v != "" {
		t.Fatalf("expected empty version, got %q", v)
	}
}

func TestPragmasApplyPerConnection(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	// Force several connections open and check each one.
	for i := 0; i < maxOpenConns; i++ {
		conn, err := st.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var fk int
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != 1 {
			t.Fatalf("conn %d: foreign_keys=%d, want 1", i, fk)
		}
		var bt int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&bt); err != nil {
			t.Fatal(err)
		}
		if bt != int(busyTimeout.Milliseconds()) {
			t.Fatalf("conn %d: busy_timeout=%d, want %d", i, bt, busyTimeout.Milliseconds())
		}
		var jm string
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&jm); err != nil {
			t.Fatal(err)
		}
		if jm != "wal" {
			t.Fatalf("conn %d: journal_mode=%q, want wal", i, jm)
		}
	}
}

func TestDefaultsAndGeneration(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()

	meta, err := st.Meta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Generation != 0 || meta.LastAppliedGeneration != 0 {
		t.Fatalf("fresh meta should be 0/0, got %+v", meta)
	}
	if meta.UpdatedAt.IsZero() {
		t.Fatal("updated_at did not parse")
	}

	net, err := st.NetworkConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if net.Mode != model.ModeA || net.TProxyPort != 12345 || net.FakeIPPool != "198.18.0.0/16" || len(net.OSPFPublishAllow) != 0 {
		t.Fatalf("unexpected network defaults: %+v", net)
	}

	dns, err := st.DNSConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dns.LocalUpstreams) != 2 || len(dns.RemoteUpstreams) != 2 || !dns.LazyCache || dns.LogLevel != "info" {
		t.Fatalf("unexpected dns defaults: %+v", dns)
	}

	var got int64
	err = st.WithTx(ctx, func(tx *sql.Tx) error {
		g, err := BumpGeneration(ctx, tx)
		got = g
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("first bump should return 1, got %d", got)
	}
	if err := st.SetLastApplied(ctx, 1); err != nil {
		t.Fatal(err)
	}
	meta, _ = st.Meta(ctx)
	if meta.Generation != 1 || meta.LastAppliedGeneration != 1 {
		t.Fatalf("meta after bump+apply: %+v", meta)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	errBoom := sql.ErrTxDone // any sentinel
	err := st.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := BumpGeneration(ctx, tx); err != nil {
			return err
		}
		return errBoom
	})
	if err != errBoom {
		t.Fatalf("expected fn error to propagate, got %v", err)
	}
	meta, _ := st.Meta(ctx)
	if meta.Generation != 0 {
		t.Fatalf("generation should have rolled back to 0, got %d", meta.Generation)
	}
}

func TestSchemaConstraints(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	db := st.DB()

	// Rules referencing a node must carry action_ref; direct rules must not.
	if _, err := db.ExecContext(ctx, `INSERT INTO rule_sets (id, name, position) VALUES ('s1','default',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rules (id,set_id,position,match_kind,match_value,action_kind) VALUES ('r1','s1',0,'domain','example.com','node')`); err == nil {
		t.Fatal("node action without action_ref should be rejected")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rules (id,set_id,position,match_kind,match_value,action_kind,action_ref) VALUES ('r1','s1',0,'domain','example.com','direct','x')`); err == nil {
		t.Fatal("direct action with action_ref should be rejected")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO rules (id,set_id,position,match_kind,match_value,action_kind) VALUES ('r1','s1',0,'domain','example.com','direct')`); err != nil {
		t.Fatalf("valid direct rule rejected: %v", err)
	}

	// Deleting the set cascades to its rules (foreign_keys must be ON).
	if _, err := db.ExecContext(ctx, `DELETE FROM rule_sets WHERE id='s1'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rules WHERE set_id='s1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected cascade delete, %d rules remain", n)
	}

	// Only one default node.
	for _, id := range []string{"n1", "n2"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id,name,protocol,address,port,is_default) VALUES (?,?,'vless','h',443,1)`, id, id); err != nil {
			if id == "n1" {
				t.Fatalf("first default node rejected: %v", err)
			}
			return
		}
	}
	t.Fatal("second default node should be rejected by nodes_single_default")
}

func TestRevertToGeneration(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	mk := func(summary, nodeName string) int64 {
		gen, err := st.Mutate(ctx, Mutation{Summary: summary}, func(tx *sql.Tx) error {
			return InsertNode(ctx, tx, NodeRow{Node: model.Node{ID: nodeName, Name: nodeName, Protocol: "vless", Address: "h", Port: 443, Enabled: true}, CredentialEnc: []byte("enc-" + nodeName)})
		})
		if err != nil {
			t.Fatal(err)
		}
		return gen
	}
	g1 := mk("add a", "a")
	mk("add b", "b")
	cs, _ := st.ListChangesets(ctx, 10)
	if len(cs) != 2 || cs[0].Diff != "snapshot" || cs[1].Generation != g1 {
		t.Fatalf("changesets %+v", cs)
	}
	newGen, restored, err := st.RevertTo(ctx, cs[1].ID, "tester")
	if err != nil || restored != g1 || newGen != 3 {
		t.Fatalf("revert %d %d %v", newGen, restored, err)
	}
	nodes, _ := st.ListNodes(ctx)
	if len(nodes) != 1 || nodes[0].ID != "a" || string(nodes[0].CredentialEnc) != "enc-a" {
		t.Fatalf("after revert %+v", nodes)
	}
	cs, _ = st.ListChangesets(ctx, 10)
	if cs[0].Summary != "revert to generation 1" || cs[1].RevertedBy == "" {
		t.Fatalf("changesets after revert %+v", cs)
	}
}
