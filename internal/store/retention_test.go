package store

import (
	"context"
	"testing"
	"time"
)

func TestPruneHistoryKeepsRecentAndRestorable(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	old := "2020-01-01T00:00:00.000Z"
	exec(`INSERT INTO events (ts, level, module, type, message) VALUES (?,'info','t','x','old')`, old)
	exec(`INSERT INTO events (level, module, type, message) VALUES ('info','t','x','new')`)
	exec(`INSERT INTO apply_log (generation, component, action, ok, ts) VALUES (1,'xray','render',1,?)`, old)
	exec(`INSERT INTO apply_log (generation, component, action, ok) VALUES (2,'xray','render',1)`)
	// 3 old changesets: with SnapshotRetention=200 they are all within the
	// restorable window and must survive.
	for i := 1; i <= 3; i++ {
		exec(`INSERT INTO changesets (id, generation, summary, created_at) VALUES (?, ?, 'x', ?)`, "cs"+string(rune('0'+i)), i, old)
	}
	exec(`INSERT INTO remote_hosts (id, name, ssh_host, ssh_port, ssh_user, ssh_auth_kind) VALUES ('h1','h','1.2.3.4',22,'root','key')`)
	exec(`INSERT INTO remote_deploys (id, host_id, kind, state, created_at) VALUES ('d-ok','h1','wg','succeeded',?)`, old)
	exec(`INSERT INTO remote_deploys (id, host_id, kind, state, created_at) VALUES ('d-bad','h1','wg','failed',?)`, old)
	exec(`INSERT INTO remote_deploy_logs (deploy_id, ts, stream, line) VALUES ('d-ok', ?, 'stdout', 'old line')`, old)
	exec(`INSERT INTO remote_deploy_logs (deploy_id, stream, line) VALUES ('d-ok', 'stdout', 'new line')`)
	exec(`INSERT INTO traffic_samples (ts, up_bytes, down_bytes) VALUES (?, 1, 1)`, time.Now().Add(-100*24*time.Hour).Unix())
	exec(`INSERT INTO traffic_samples (ts, up_bytes, down_bytes) VALUES (?, 1, 1)`, time.Now().Unix())

	rep, err := s.PruneHistory(ctx, 30*24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := PruneReport{Events: 1, ApplyLog: 1, Changesets: 0, Deploys: 1, DeployLogs: 1, Traffic: 1}
	if rep != want {
		t.Fatalf("report %+v, want %+v", rep, want)
	}
	u, err := s.HistoryUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.Events != 1 || u.ApplyLog != 1 || u.Changesets != 3 || u.DeployLogs != 1 || u.TrafficSamples != 1 {
		t.Fatalf("usage after prune: %+v", u)
	}
	var deploys int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_deploys`).Scan(&deploys); err != nil || deploys != 1 {
		t.Fatalf("succeeded deploy must survive: %d %v", deploys, err)
	}
	if err := s.Compact(ctx); err != nil {
		t.Fatal(err)
	}
}
