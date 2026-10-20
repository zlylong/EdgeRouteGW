// Package reconcile is the single writer to the outside world. It watches the
// desired-state generation, renders artifacts, asks each driver for a plan and
// applies it. Writes from the API only ever bump the generation and call
// Notify; nothing else touches Xray, Mosdns or nftables.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Status is the reconciler's externally visible state.
type Status struct {
	Applying       bool                    `json:"applying"`
	AppliedGen     int64                   `json:"applied_generation"`
	LastAttemptAt  time.Time               `json:"last_attempt_at"`
	LastSuccessAt  time.Time               `json:"last_success_at"`
	LastError      string                  `json:"last_error,omitempty"`
	Components     []model.ComponentHealth `json:"components"`
	Warnings       []string                `json:"warnings"`
	LastPlans      []driver.Plan           `json:"last_plans"`
	RetryInSeconds int                     `json:"retry_in_s,omitempty"`
}

// Reconciler drives the apply loop.
type Reconciler struct {
	Store   *store.Store
	Keyring *secret.Keyring
	Render  render.Deps
	Drivers []driver.Driver
	Bus     *events.Bus
	Log     *slog.Logger

	// Coalesce is how long to wait after a notification for more writes
	// before applying. Resync is the periodic drift check.
	Coalesce time.Duration
	Resync   time.Duration
	// MaxBackoff caps the retry delay after a failed apply.
	MaxBackoff time.Duration

	notify chan struct{}
	runMu  sync.Mutex // serialises reconcile passes

	mu       sync.RWMutex
	status   Status
	lastGood map[model.Component]*render.Artifacts
	backoff  time.Duration
	retryAt  time.Time
}

// New wires a reconciler with production timings.
func New(st *store.Store, kr *secret.Keyring, deps render.Deps, drivers []driver.Driver, bus *events.Bus, log *slog.Logger) *Reconciler {
	r := &Reconciler{
		Store: st, Keyring: kr, Render: deps, Drivers: drivers, Bus: bus, Log: log,
		Coalesce: 300 * time.Millisecond, Resync: 30 * time.Second, MaxBackoff: 60 * time.Second,
		notify:   make(chan struct{}, 1),
		lastGood: map[model.Component]*render.Artifacts{},
	}
	r.status.Components = r.unknownHealth()
	r.status.Warnings = []string{}
	r.status.LastPlans = []driver.Plan{}
	return r
}

func (r *Reconciler) unknownHealth() []model.ComponentHealth {
	out := make([]model.ComponentHealth, 0, len(r.Drivers))
	for _, d := range r.Drivers {
		out = append(out, model.ComponentHealth{Name: d.Component(), Status: model.StatusUnknown})
	}
	return out
}

// Notify tells the loop that desired state changed. It never blocks.
func (r *Reconciler) Notify() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// Status returns a copy of the current status.
func (r *Reconciler) Status() Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := r.status
	if !r.retryAt.IsZero() && time.Now().Before(r.retryAt) {
		s.RetryInSeconds = int(time.Until(r.retryAt).Seconds()) + 1
	}
	return s
}

// Run executes the loop until ctx is done. It performs one pass immediately
// so a restarted service converges without waiting for a write.
func (r *Reconciler) Run(ctx context.Context) error {
	r.pass(ctx)
	resync := time.NewTicker(r.Resync)
	defer resync.Stop()
	for {
		var retry <-chan time.Time
		r.mu.RLock()
		if !r.retryAt.IsZero() {
			retry = time.After(time.Until(r.retryAt))
		}
		r.mu.RUnlock()

		select {
		case <-ctx.Done():
			return nil
		case <-r.notify:
			r.coalesce(ctx)
			r.pass(ctx)
		case <-resync.C:
			r.pass(ctx)
		case <-retry:
			r.pass(ctx)
		}
	}
}

// coalesce absorbs further notifications for the coalescing window, so a
// burst of UI writes (drag-reordering twenty rules) becomes one apply.
func (r *Reconciler) coalesce(ctx context.Context) {
	if r.Coalesce <= 0 {
		return
	}
	deadline := time.NewTimer(r.Coalesce)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.notify:
			// keep absorbing until the window closes
		case <-deadline.C:
			return
		}
	}
}

// ReconcileNow runs one synchronous pass (API "apply now", tests).
func (r *Reconciler) ReconcileNow(ctx context.Context) error {
	return r.pass(ctx)
}

// PlanOnly renders the current desired state and returns each driver's plan
// without applying anything.
func (r *Reconciler) PlanOnly(ctx context.Context) ([]driver.Plan, *render.Artifacts, error) {
	snap, err := desired.Load(ctx, r.Store, r.Keyring)
	if err != nil {
		return nil, nil, err
	}
	arts, err := render.Render(snap, r.Render)
	if err != nil {
		return nil, nil, err
	}
	r.mu.RLock()
	prev := copyMap(r.lastGood)
	r.mu.RUnlock()
	var plans []driver.Plan
	for _, d := range r.Drivers {
		p, err := d.Plan(ctx, prev[d.Component()], arts)
		if err != nil {
			p = driver.Plan{Component: d.Component(), Actions: []driver.Action{{Kind: driver.ActionNoop, Detail: "plan error: " + err.Error()}}}
		}
		plans = append(plans, p)
	}
	return plans, arts, nil
}

func copyMap(m map[model.Component]*render.Artifacts) map[model.Component]*render.Artifacts {
	c := make(map[model.Component]*render.Artifacts, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (r *Reconciler) pass(ctx context.Context) error {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	start := time.Now()
	r.mu.Lock()
	r.status.Applying = true
	r.status.LastAttemptAt = start
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.status.Applying = false
		r.mu.Unlock()
	}()

	snap, err := desired.Load(ctx, r.Store, r.Keyring)
	if err != nil {
		return r.fail(ctx, 0, fmt.Errorf("load desired state: %w", err), nil)
	}
	arts, err := render.Render(snap, r.Render)
	if err != nil {
		return r.fail(ctx, snap.Generation, fmt.Errorf("render: %w", err), nil)
	}
	r.Bus.Publish(events.ApplyStarted, map[string]any{"generation": snap.Generation})

	var plans []driver.Plan
	var errs []error
	for _, d := range r.Drivers {
		comp := d.Component()
		r.mu.RLock()
		prev := r.lastGood[comp]
		r.mu.RUnlock()

		plan, perr := d.Plan(ctx, prev, arts)
		if perr != nil {
			errs = append(errs, fmt.Errorf("%s: plan: %w", comp, perr))
			plans = append(plans, driver.Plan{Component: comp, Actions: []driver.Action{{Kind: driver.ActionNoop, Detail: "plan failed: " + perr.Error()}}})
			_ = r.Store.RecordApply(ctx, model.ApplyLogEntry{Generation: snap.Generation, Component: comp, Action: "plan", OK: false, Error: perr.Error()})
			continue
		}
		plans = append(plans, plan)
		if !plan.Changes() {
			r.setGood(comp, arts)
			continue
		}
		t0 := time.Now()
		aerr := d.Apply(ctx, plan, arts)
		entry := model.ApplyLogEntry{Generation: snap.Generation, Component: comp, Action: plan.Summary(), OK: aerr == nil, DurationMs: time.Since(t0).Milliseconds()}
		if aerr != nil {
			entry.Error = aerr.Error()
			errs = append(errs, fmt.Errorf("%s: %w", comp, aerr))
			r.Log.Error("apply failed", "component", comp, "plan", plan.Summary(), "err", aerr)
		} else {
			r.setGood(comp, arts)
			r.Log.Info("applied", "component", comp, "plan", plan.Summary(), "generation", snap.Generation, "dur_ms", entry.DurationMs)
		}
		_ = r.Store.RecordApply(ctx, entry)
	}

	health := make([]model.ComponentHealth, 0, len(r.Drivers))
	for _, d := range r.Drivers {
		health = append(health, d.Health(ctx))
	}

	r.mu.Lock()
	r.status.Components = health
	r.status.Warnings = arts.Warnings
	r.status.LastPlans = plans
	r.mu.Unlock()
	r.Bus.Publish(events.ComponentHealth, health)

	if len(errs) > 0 {
		return r.fail(ctx, snap.Generation, errors.Join(errs...), plans)
	}
	if err := r.Store.SetLastApplied(ctx, snap.Generation); err != nil {
		return r.fail(ctx, snap.Generation, fmt.Errorf("record applied generation: %w", err), plans)
	}
	r.mu.Lock()
	r.status.AppliedGen = snap.Generation
	r.status.LastSuccessAt = time.Now()
	r.status.LastError = ""
	r.backoff = 0
	r.retryAt = time.Time{}
	r.mu.Unlock()

	changed := 0
	for _, p := range plans {
		if p.Changes() {
			changed++
		}
	}
	if changed > 0 {
		_ = r.Store.AddEvent(ctx, model.Event{Level: model.LevelInfo, Module: "reconcile", Type: "apply.finished",
			Message: fmt.Sprintf("generation %d applied to %d component(s) in %s", snap.Generation, changed, time.Since(start).Round(time.Millisecond))})
	}
	r.Bus.Publish(events.ApplyFinished, map[string]any{"generation": snap.Generation, "plans": plans, "warnings": arts.Warnings, "duration_ms": time.Since(start).Milliseconds()})
	return nil
}

func (r *Reconciler) setGood(comp model.Component, arts *render.Artifacts) {
	r.mu.Lock()
	r.lastGood[comp] = arts
	r.mu.Unlock()
}

func (r *Reconciler) fail(ctx context.Context, gen int64, err error, plans []driver.Plan) error {
	r.mu.Lock()
	if r.backoff == 0 {
		r.backoff = time.Second
	} else {
		r.backoff *= 2
		if r.backoff > r.MaxBackoff {
			r.backoff = r.MaxBackoff
		}
	}
	r.retryAt = time.Now().Add(r.backoff)
	r.status.LastError = err.Error()
	if plans != nil {
		r.status.LastPlans = plans
	}
	backoff := r.backoff
	r.mu.Unlock()
	r.Log.Error("reconcile failed", "generation", gen, "err", err, "retry_in", backoff)
	_ = r.Store.AddEvent(ctx, model.Event{Level: model.LevelError, Module: "reconcile", Type: "apply.failed",
		Message: fmt.Sprintf("generation %d: %v", gen, err), Details: fmt.Sprintf(`{"retry_in_s":%d}`, int(backoff.Seconds()))})
	r.Bus.Publish(events.ApplyFailed, map[string]any{"generation": gen, "error": err.Error(), "retry_in_s": int(backoff.Seconds())})
	return err
}
