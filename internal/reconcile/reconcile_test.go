package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// fakeDriver applies whenever the artifact bytes differ from what it last saw.
type fakeDriver struct {
	comp    model.Component
	applies atomic.Int32
	plans   atomic.Int32
	failN   atomic.Int32 // fail this many applies
	seen    []byte
}

func (f *fakeDriver) Component() model.Component { return f.comp }
func (f *fakeDriver) Plan(_ context.Context, prev, next *render.Artifacts) (driver.Plan, error) {
	f.plans.Add(1)
	if prev != nil && string(prev.Xray) == string(next.Xray) && string(prev.Nft) == string(next.Nft) {
		return driver.Plan{Component: f.comp, Actions: []driver.Action{{Kind: driver.ActionNoop}}}, nil
	}
	return driver.Plan{Component: f.comp, Actions: []driver.Action{{Kind: driver.ActionWrite}}}, nil
}
func (f *fakeDriver) Apply(_ context.Context, _ driver.Plan, next *render.Artifacts) error {
	f.applies.Add(1)
	if f.failN.Load() > 0 {
		f.failN.Add(-1)
		return errors.New("boom")
	}
	f.seen = next.Xray
	return nil
}
func (f *fakeDriver) Health(context.Context) model.ComponentHealth {
	return model.ComponentHealth{Name: f.comp, Status: model.StatusActive}
}

func setup(t *testing.T) (*Reconciler, *store.Store, *fakeDriver) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fd := &fakeDriver{comp: model.ComponentXray}
	r := New(st, nil, render.Deps{Paths: paths.New(t.TempDir())}, []driver.Driver{fd}, events.New(16), slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Coalesce = 100 * time.Millisecond
	r.Resync = time.Hour
	return r, st, fd
}

func addRule(t *testing.T, st *store.Store, value string) {
	t.Helper()
	ctx := context.Background()
	_, err := st.Mutate(ctx, store.Mutation{Summary: "add " + value}, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO rule_sets (id,name,position) VALUES ('s','s',0)`); err != nil {
			return err
		}
		return store.InsertRule(ctx, tx, model.Rule{ID: value, SetID: "s", MatchKind: model.MatchDomain, MatchValue: value, ActionKind: model.ActionDirect, Enabled: true})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFirstPassAppliesAndRecords(t *testing.T) {
	r, st, fd := setup(t)
	ctx := context.Background()
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatal(err)
	}
	if fd.applies.Load() != 1 {
		t.Fatalf("first pass should apply once, got %d", fd.applies.Load())
	}
	meta, _ := st.Meta(ctx)
	if meta.LastAppliedGeneration != 0 || r.Status().AppliedGen != 0 {
		t.Fatalf("applied gen should be 0: %+v", meta)
	}
	// Second pass with nothing changed is a no-op.
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatal(err)
	}
	if fd.applies.Load() != 1 {
		t.Fatalf("unchanged state must not re-apply, got %d", fd.applies.Load())
	}
	addRule(t, st, "a.com")
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatal(err)
	}
	if fd.applies.Load() != 2 {
		t.Fatalf("changed state must apply, got %d", fd.applies.Load())
	}
	meta, _ = st.Meta(ctx)
	if meta.Generation != 1 || meta.LastAppliedGeneration != 1 {
		t.Fatalf("meta %+v", meta)
	}
	logs, _ := st.ListApplyLog(ctx, 0, 10)
	if len(logs) != 2 || !logs[0].OK || logs[0].Component != model.ComponentXray {
		t.Fatalf("apply log %+v", logs)
	}
}

func TestBurstCoalescesIntoOneApply(t *testing.T) {
	r, st, fd := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()

	// Wait for the initial pass.
	deadline := time.Now().Add(2 * time.Second)
	for fd.applies.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fd.applies.Load() != 1 {
		t.Fatalf("initial pass missing: %d", fd.applies.Load())
	}

	// Twenty writes land, then a burst of notifications (what twenty quick
	// API calls produce). The writes themselves are not timed: under -race
	// they are slow enough to outlast a short window, which is not what the
	// test is about.
	for i := 0; i < 20; i++ {
		addRule(t, st, "d"+string(rune('a'+i))+".com")
	}
	for i := 0; i < 20; i++ {
		r.Notify()
	}
	// Let the coalescing window close and the pass run.
	time.Sleep(600 * time.Millisecond)
	cancel()
	<-done
	if got := fd.applies.Load(); got != 2 {
		t.Fatalf("20 writes should produce exactly one more apply, got %d total", got)
	}
	meta, _ := st.Meta(context.Background())
	if meta.Generation != 20 || meta.LastAppliedGeneration != 20 {
		t.Fatalf("meta %+v", meta)
	}
}

func TestFailureSetsBackoffAndRetries(t *testing.T) {
	r, st, fd := setup(t)
	ctx := context.Background()
	fd.failN.Store(1)
	if err := r.ReconcileNow(ctx); err == nil {
		t.Fatal("expected failure")
	}
	s := r.Status()
	if s.LastError == "" || s.RetryInSeconds == 0 {
		t.Fatalf("status after failure %+v", s)
	}
	meta, _ := st.Meta(ctx)
	if meta.LastAppliedGeneration != 0 {
		t.Fatal("failed apply must not advance last_applied")
	}
	evs, _ := st.ListEvents(ctx, store.EventFilter{Module: "reconcile"})
	if len(evs) != 1 || evs[0].Level != model.LevelError {
		t.Fatalf("events %+v", evs)
	}
	// Next pass retries the same artifacts because lastGood was not updated.
	if err := r.ReconcileNow(ctx); err != nil {
		t.Fatal(err)
	}
	if fd.applies.Load() != 2 || r.Status().LastError != "" || r.Status().RetryInSeconds != 0 {
		t.Fatalf("retry did not clear failure: applies=%d status=%+v", fd.applies.Load(), r.Status())
	}
}

func TestPlanOnlyHasNoSideEffects(t *testing.T) {
	r, _, fd := setup(t)
	plans, arts, err := r.PlanOnly(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || !plans[0].Changes() || arts == nil {
		t.Fatalf("plans %+v", plans)
	}
	if fd.applies.Load() != 0 {
		t.Fatal("PlanOnly must not apply")
	}
}
