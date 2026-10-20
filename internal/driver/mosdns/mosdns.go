// Package mosdns drives Mosdns. It has no reload API, so every content change
// is a restart; the driver rate-limits restarts (minimum spacing) instead of
// skipping them, so a burst of edits yields one restart and never a stale
// config.
package mosdns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// Driver implements driver.Driver for Mosdns.
type Driver struct {
	Paths   paths.Paths
	Unit    string
	Systemd systemd.Manager
	Log     *slog.Logger
	// MinRestartGap is the minimum spacing between two restarts. Apply waits
	// out the remainder rather than skipping.
	MinRestartGap time.Duration
	// Sleep is the wait primitive (overridable in tests).
	Sleep func(ctx context.Context, d time.Duration) error

	lastRestart time.Time
	lastErr     string
	lastApply   time.Time
}

// New builds a driver with the production unit name.
func New(p paths.Paths, sd systemd.Manager, log *slog.Logger) *Driver {
	return &Driver{Paths: p, Unit: "edgeroutegw-mosdns.service", Systemd: sd, Log: log, MinRestartGap: 10 * time.Second, Sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Component implements driver.Driver.
func (d *Driver) Component() model.Component { return model.ComponentMosdns }

func (d *Driver) files(next *render.Artifacts) map[string][]byte {
	m := map[string][]byte{
		d.Paths.MosdnsConfig():    next.MosdnsConfig,
		d.Paths.MosdnsProxyList(): next.MosdnsProxyDomains,
	}
	if next.MosdnsCNDomains != nil {
		m[d.Paths.MosdnsCNList()] = next.MosdnsCNDomains
	}
	return m
}

// Plan implements driver.Driver.
func (d *Driver) Plan(ctx context.Context, _, next *render.Artifacts) (driver.Plan, error) {
	plan := driver.Plan{Component: model.ComponentMosdns}
	changed := false
	for path, content := range d.files(next) {
		if !driver.SameContent(path, content) {
			changed = true
			plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionWrite, Detail: path})
			if path == d.Paths.MosdnsConfig() {
				old, _ := os.ReadFile(path)
				plan.Diff = driver.LineDiff(old, content, 100)
			}
		}
	}
	// The CN list is referenced by config.yaml and must exist even when no
	// geodata is installed yet.
	if next.MosdnsCNDomains == nil {
		if _, err := os.Stat(d.Paths.MosdnsCNList()); errors.Is(err, os.ErrNotExist) {
			plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionWrite, Detail: d.Paths.MosdnsCNList() + " (empty placeholder)"})
		}
	}
	state, err := d.Systemd.State(ctx, d.Unit)
	if err != nil {
		return plan, fmt.Errorf("mosdns unit state: %w", err)
	}
	switch {
	case !state.Active():
		if d.binaryMissing() {
			plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionSkipped, Detail: "mosdns binary not installed"})
		} else {
			plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionStart, Detail: d.Unit})
		}
	case changed:
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionRestart, Detail: "config or domain list changed"})
	default:
		if len(plan.Actions) == 0 {
			plan.Actions = []driver.Action{{Kind: driver.ActionNoop, Detail: "config unchanged, unit active"}}
		}
	}
	return plan, nil
}

// Apply implements driver.Driver.
func (d *Driver) Apply(ctx context.Context, plan driver.Plan, next *render.Artifacts) error {
	d.lastApply = time.Now()
	err := d.apply(ctx, plan, next)
	if err != nil {
		d.lastErr = err.Error()
	} else {
		d.lastErr = ""
	}
	return err
}

func (d *Driver) apply(ctx context.Context, plan driver.Plan, next *render.Artifacts) error {
	wrote := false
	for _, a := range plan.Actions {
		if a.Kind == driver.ActionWrite && !wrote {
			wrote = true
			if err := os.MkdirAll(d.Paths.RunDir, 0o755); err != nil {
				return fmt.Errorf("create runtime dir: %w", err)
			}
			for path, content := range d.files(next) {
				if _, err := driver.WriteFileAtomic(path, content, 0o644); err != nil {
					return fmt.Errorf("write %s: %w", path, err)
				}
			}
			if next.MosdnsCNDomains == nil {
				if _, err := os.Stat(d.Paths.MosdnsCNList()); errors.Is(err, os.ErrNotExist) {
					if _, err := driver.WriteFileAtomic(d.Paths.MosdnsCNList(), []byte{}, 0o644); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, a := range plan.Actions {
		switch a.Kind {
		case driver.ActionStart:
			if err := d.Systemd.Start(ctx, d.Unit); err != nil {
				return err
			}
			d.lastRestart = time.Now()
		case driver.ActionRestart:
			if wait := d.MinRestartGap - time.Since(d.lastRestart); wait > 0 && !d.lastRestart.IsZero() {
				d.Log.Info("mosdns restart rate-limited, waiting", "wait", wait.Round(time.Millisecond))
				if err := d.Sleep(ctx, wait); err != nil {
					return err
				}
			}
			if err := d.Systemd.Restart(ctx, d.Unit); err != nil {
				return err
			}
			d.lastRestart = time.Now()
		}
	}
	return nil
}

func (d *Driver) binaryMissing() bool {
	_, err := os.Stat(d.Paths.MosdnsBinary())
	return errors.Is(err, os.ErrNotExist)
}

// Health implements driver.Driver.
func (d *Driver) Health(ctx context.Context) model.ComponentHealth {
	h := model.ComponentHealth{Name: model.ComponentMosdns, Status: model.StatusUnknown, LastError: d.lastErr}
	if !d.lastApply.IsZero() {
		t := d.lastApply
		h.LastApply = &t
	}
	if d.binaryMissing() {
		h.Status = model.StatusDown
		if h.LastError == "" {
			h.LastError = "mosdns binary not installed"
		}
		return h
	}
	st, err := d.Systemd.State(ctx, d.Unit)
	switch {
	case err != nil:
		h.LastError = err.Error()
	case !st.Active():
		h.Status = model.StatusDown
	case d.lastErr != "":
		h.Status = model.StatusDegraded
	default:
		h.Status = model.StatusActive
	}
	return h
}
