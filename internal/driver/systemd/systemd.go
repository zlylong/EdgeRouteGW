// Package systemd controls units over D-Bus. The Manager interface is what
// drivers use; the D-Bus implementation is the production one and Fake stands
// in for tests.
package systemd

import (
	"context"
	"fmt"
	"sync"
	"time"

	sd "github.com/coreos/go-systemd/v22/dbus"
)

// UnitState is the subset of unit properties the reconciler cares about.
type UnitState struct {
	Name        string
	Loaded      bool
	ActiveState string // active, inactive, failed, activating, deactivating
	SubState    string // running, dead, exited, ...
	NRestarts   uint32
}

// Active reports whether the unit is up.
func (u UnitState) Active() bool { return u.ActiveState == "active" }

// Manager is the systemd surface the drivers need.
type Manager interface {
	State(ctx context.Context, unit string) (UnitState, error)
	Restart(ctx context.Context, unit string) error
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
}

// DBus talks to the system bus. One connection is shared and reconnected on
// error.
type DBus struct {
	mu   sync.Mutex
	conn *sd.Conn
	// JobTimeout bounds how long Restart/Start/Stop wait for the job result.
	JobTimeout time.Duration
}

// NewDBus returns a lazily connecting manager.
func NewDBus() *DBus { return &DBus{JobTimeout: 30 * time.Second} }

func (d *DBus) connect(ctx context.Context) (*sd.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		return d.conn, nil
	}
	c, err := sd.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to systemd: %w", err)
	}
	d.conn = c
	return c, nil
}

func (d *DBus) reset() {
	d.mu.Lock()
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
	d.mu.Unlock()
}

// State implements Manager.
func (d *DBus) State(ctx context.Context, unit string) (UnitState, error) {
	c, err := d.connect(ctx)
	if err != nil {
		return UnitState{}, err
	}
	props, err := c.GetUnitPropertiesContext(ctx, unit)
	if err != nil {
		d.reset()
		return UnitState{}, fmt.Errorf("get properties of %s: %w", unit, err)
	}
	st := UnitState{Name: unit}
	st.ActiveState, _ = props["ActiveState"].(string)
	st.SubState, _ = props["SubState"].(string)
	load, _ := props["LoadState"].(string)
	st.Loaded = load == "loaded"
	if sp, err := c.GetServicePropertyContext(ctx, unit, "NRestarts"); err == nil {
		if v, ok := sp.Value.Value().(uint32); ok {
			st.NRestarts = v
		}
	}
	return st, nil
}

func (d *DBus) job(ctx context.Context, unit string, start func(*sd.Conn, chan<- string) (int, error)) error {
	c, err := d.connect(ctx)
	if err != nil {
		return err
	}
	ch := make(chan string, 1)
	if _, err := start(c, ch); err != nil {
		d.reset()
		return fmt.Errorf("%s: %w", unit, err)
	}
	timeout := d.JobTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	select {
	case res := <-ch:
		if res != "done" {
			return fmt.Errorf("%s: job result %q", unit, res)
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("%s: job timed out after %s", unit, timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Restart implements Manager.
func (d *DBus) Restart(ctx context.Context, unit string) error {
	return d.job(ctx, unit, func(c *sd.Conn, ch chan<- string) (int, error) {
		return c.RestartUnitContext(ctx, unit, "replace", ch)
	})
}

// Start implements Manager.
func (d *DBus) Start(ctx context.Context, unit string) error {
	return d.job(ctx, unit, func(c *sd.Conn, ch chan<- string) (int, error) {
		return c.StartUnitContext(ctx, unit, "replace", ch)
	})
}

// Stop implements Manager.
func (d *DBus) Stop(ctx context.Context, unit string) error {
	return d.job(ctx, unit, func(c *sd.Conn, ch chan<- string) (int, error) {
		return c.StopUnitContext(ctx, unit, "replace", ch)
	})
}

// Fake is an in-memory Manager for tests.
type Fake struct {
	mu       sync.Mutex
	units    map[string]UnitState
	Restarts map[string]int
	// Fail, if set, makes every operation on that unit return an error.
	Fail map[string]error
}

// NewFake creates a Fake where the listed units are active.
func NewFake(activeUnits ...string) *Fake {
	f := &Fake{units: map[string]UnitState{}, Restarts: map[string]int{}, Fail: map[string]error{}}
	for _, u := range activeUnits {
		f.units[u] = UnitState{Name: u, Loaded: true, ActiveState: "active", SubState: "running"}
	}
	return f
}

// SetActive marks a unit active or inactive.
func (f *Fake) SetActive(unit string, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := UnitState{Name: unit, Loaded: true, ActiveState: "inactive", SubState: "dead"}
	if active {
		st.ActiveState, st.SubState = "active", "running"
	}
	f.units[unit] = st
}

// State implements Manager.
func (f *Fake) State(_ context.Context, unit string) (UnitState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.Fail[unit]; err != nil {
		return UnitState{}, err
	}
	st, ok := f.units[unit]
	if !ok {
		return UnitState{Name: unit, ActiveState: "inactive", SubState: "dead"}, nil
	}
	return st, nil
}

// Restart implements Manager.
func (f *Fake) Restart(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.Fail[unit]; err != nil {
		return err
	}
	f.Restarts[unit]++
	st := f.units[unit]
	st.Name, st.Loaded, st.ActiveState, st.SubState = unit, true, "active", "running"
	st.NRestarts++
	f.units[unit] = st
	return nil
}

// Start implements Manager.
func (f *Fake) Start(ctx context.Context, unit string) error { return f.Restart(ctx, unit) }

// Stop implements Manager.
func (f *Fake) Stop(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.Fail[unit]; err != nil {
		return err
	}
	f.units[unit] = UnitState{Name: unit, Loaded: true, ActiveState: "inactive", SubState: "dead"}
	return nil
}
