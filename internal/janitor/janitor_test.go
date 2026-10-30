package janitor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/observe"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func newTestJanitor(t *testing.T) (*Janitor, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "xray_access.log")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lc := observe.NewLogCap(log, logPath)
	j := New(st, lc, nil, nil, dir, log)
	j.JournalDirs = []string{filepath.Join(dir, "no-journal")}
	return j, logPath
}

func TestSettingsRoundTripAndValidation(t *testing.T) {
	j, _ := newTestJanitor(t)
	ctx := context.Background()
	if got := j.Settings(ctx); got != Defaults {
		t.Fatalf("defaults: %+v", got)
	}
	if _, err := j.SetSettings(ctx, Settings{HistoryDays: 0, TrafficDays: 90, RunLogMB: 64, JournalDays: 30, JournalMB: 500}); err == nil {
		t.Fatal("history_days=0 accepted")
	}
	want := Settings{HistoryDays: 7, TrafficDays: 14, RunLogMB: 16, JournalDays: 3, JournalMB: 100}
	if _, err := j.SetSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got := j.Settings(ctx); got != want {
		t.Fatalf("saved %+v, want %+v", got, want)
	}
	if j.LogCap.MaxBytes() != 16<<20 {
		t.Fatalf("log cap not applied: %d", j.LogCap.MaxBytes())
	}
}

func TestCleanupClearsRuntimeLogsAndReports(t *testing.T) {
	j, logPath := newTestJanitor(t)
	ctx := context.Background()
	if err := os.WriteFile(logPath, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := j.Cleanup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RuntimeLogBytesFreed != 4096 {
		t.Fatalf("freed %d", rep.RuntimeLogBytesFreed)
	}
	if st, _ := os.Stat(logPath); st.Size() != 0 {
		t.Fatalf("log not cleared: %d", st.Size())
	}
	u, err := j.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.LastPruneAt == nil || u.JournalManaged || len(u.RuntimeLogs) != 1 {
		t.Fatalf("usage: %+v", u)
	}
}
