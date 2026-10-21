// Package frr drives FRRouting: it keeps /etc/frr/frr.conf and the daemons
// file in line with the rendered config, restarts the unit when they change,
// and reads OSPF neighbour state for the UI. In Mode A FRR is stopped.
//
// The kernel routes FRR redistributes are owned by the route driver; this
// driver wraps it so the "frr" component reports one health and one plan.
package frr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/driver/route"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// Driver implements driver.Driver for FRR + published routes.
type Driver struct {
	ConfDir string // /etc/frr
	Unit    string // frr.service
	Run     execx.Runner
	Systemd systemd.Manager
	Routes  *route.Driver
	Log     *slog.Logger

	lastErr   string
	lastApply time.Time
	mode      model.Mode
}

// New builds the production driver.
func New(run execx.Runner, sd systemd.Manager, routes *route.Driver, log *slog.Logger) *Driver {
	return &Driver{ConfDir: "/etc/frr", Unit: "frr.service", Run: run, Systemd: sd, Routes: routes, Log: log}
}

// Component implements driver.Driver.
func (d *Driver) Component() model.Component { return model.ComponentFRR }

func (d *Driver) confPath() string    { return filepath.Join(d.ConfDir, "frr.conf") }
func (d *Driver) daemonsPath() string { return filepath.Join(d.ConfDir, "daemons") }

type planState struct {
	routes driver.Plan
}

// Plan implements driver.Driver.
func (d *Driver) Plan(ctx context.Context, prev, next *render.Artifacts) (driver.Plan, error) {
	plan := driver.Plan{Component: model.ComponentFRR}
	st := &planState{}
	plan.State = st
	installed := d.installed()
	// Health reports by mode; record it here too, since a restart with
	// nothing to apply never reaches Apply and would show "disabled" while
	// OSPF is running.
	d.mode = next.Mode

	if next.Mode == model.ModeA {
		// Withdraw routes, stop FRR if it is running.
		rp, err := d.Routes.Plan(ctx, prev, next)
		if err != nil && installed {
			return plan, err
		}
		st.routes = rp
		if rp.Changes() {
			plan.Actions = append(plan.Actions, rp.Actions...)
		}
		if installed {
			if state, err := d.Systemd.State(ctx, d.Unit); err == nil && state.Active() {
				plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionExec, Detail: "stop " + d.Unit + " (Mode A)"})
			}
		}
		if len(plan.Actions) == 0 {
			plan.Actions = []driver.Action{{Kind: driver.ActionSkipped, Detail: "not used in Mode A"}}
		}
		return plan, nil
	}

	if !installed {
		plan.Actions = []driver.Action{{Kind: driver.ActionSkipped, Detail: "frr is not installed (apt install frr)"}}
		return plan, nil
	}
	changed := false
	if !driver.SameContent(d.confPath(), next.FRR) {
		changed = true
		old, _ := os.ReadFile(d.confPath())
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionWrite, Detail: d.confPath()})
		plan.Diff = driver.LineDiff(old, next.FRR, 60)
	}
	if !d.daemonsEnableOSPF(false) {
		changed = true
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionWrite, Detail: d.daemonsPath() + " (ospfd=yes)"})
	}
	state, err := d.Systemd.State(ctx, d.Unit)
	if err != nil {
		return plan, fmt.Errorf("frr unit state: %w", err)
	}
	switch {
	case !state.Active():
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionStart, Detail: d.Unit})
	case changed:
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionRestart, Detail: "frr.conf or daemons changed"})
	}
	rp, err := d.Routes.Plan(ctx, prev, next)
	if err != nil {
		return plan, err
	}
	st.routes = rp
	if rp.Changes() {
		plan.Actions = append(plan.Actions, rp.Actions...)
		if plan.Diff == "" {
			plan.Diff = rp.Diff
		}
	}
	if len(plan.Actions) == 0 {
		plan.Actions = []driver.Action{{Kind: driver.ActionNoop, Detail: "config and routes unchanged, unit active"}}
	}
	return plan, nil
}

// Apply implements driver.Driver.
func (d *Driver) Apply(ctx context.Context, plan driver.Plan, next *render.Artifacts) error {
	d.lastApply = time.Now()
	d.mode = next.Mode
	err := d.apply(ctx, plan, next)
	if err != nil {
		d.lastErr = err.Error()
	} else {
		d.lastErr = ""
	}
	return err
}

func (d *Driver) apply(ctx context.Context, plan driver.Plan, next *render.Artifacts) error {
	st, _ := plan.State.(*planState)
	// Routes first: with FRR not yet running they sit in table 100 until it
	// imports them; with FRR running they are picked up immediately.
	if st != nil && st.routes.Changes() {
		if err := d.Routes.Apply(ctx, st.routes, next); err != nil {
			return err
		}
	}
	for _, a := range plan.Actions {
		switch {
		case a.Kind == driver.ActionWrite && a.Detail == d.confPath():
			if _, err := driver.WriteFileAtomic(d.confPath(), next.FRR, 0o640); err != nil {
				return fmt.Errorf("write frr.conf: %w", err)
			}
			d.chownFRR(d.confPath())
		case a.Kind == driver.ActionWrite && strings.HasPrefix(a.Detail, d.daemonsPath()):
			if !d.daemonsEnableOSPF(true) {
				return fmt.Errorf("enable ospfd in %s", d.daemonsPath())
			}
		case a.Kind == driver.ActionStart:
			if err := d.Systemd.Start(ctx, d.Unit); err != nil {
				return err
			}
		case a.Kind == driver.ActionRestart:
			if err := d.Systemd.Restart(ctx, d.Unit); err != nil {
				return err
			}
		case a.Kind == driver.ActionExec && strings.HasPrefix(a.Detail, "stop "):
			if err := d.Systemd.Stop(ctx, d.Unit); err != nil {
				return err
			}
		}
	}
	return nil
}

// chownFRR hands the config to the frr user when it exists; FRR refuses to
// read a root-owned file in some packagings. Best effort.
func (d *Driver) chownFRR(path string) {
	if d.Run == nil {
		return
	}
	_, _ = d.Run.Run(context.Background(), "chown", "frr:frr", path)
}

var ospfdLine = regexp.MustCompile(`(?m)^ospfd=.*$`)

// daemonsEnableOSPF reports whether /etc/frr/daemons has ospfd=yes; with
// write it rewrites the line (or appends it).
func (d *Driver) daemonsEnableOSPF(write bool) bool {
	b, err := os.ReadFile(d.daemonsPath())
	if err != nil {
		if !write {
			return false
		}
		b = nil
	}
	if ospfdLine.Match(b) && strings.Contains(string(ospfdLine.Find(b)), "=yes") {
		return true
	}
	if !write {
		return false
	}
	var out string
	if ospfdLine.Match(b) {
		out = ospfdLine.ReplaceAllString(string(b), "ospfd=yes")
	} else {
		out = strings.TrimRight(string(b), "\n") + "\nospfd=yes\n"
	}
	if _, err := driver.WriteFileAtomic(d.daemonsPath(), []byte(out), 0o640); err != nil {
		return false
	}
	d.chownFRR(d.daemonsPath())
	return true
}

func (d *Driver) installed() bool {
	_, err := os.Stat(d.ConfDir)
	return !errors.Is(err, os.ErrNotExist)
}

// Neighbor is one OSPF adjacency.
type Neighbor struct {
	RouterID  string `json:"router_id"`
	Address   string `json:"address"`
	Interface string `json:"interface"`
	State     string `json:"state"`
	Priority  int    `json:"priority"`
	DeadTime  string `json:"dead_time"`
}

// Neighbors reads "show ip ospf neighbor json" via vtysh.
func (d *Driver) Neighbors(ctx context.Context) ([]Neighbor, error) {
	if d.Run == nil {
		return nil, errors.New("no runner")
	}
	res, err := d.Run.Run(ctx, "vtysh", "-c", "show ip ospf neighbor json")
	if err != nil {
		return nil, err
	}
	return ParseNeighbors(res.Output)
}

// ParseNeighbors decodes FRR's neighbor JSON (both the 8.x "neighbors" map
// and the newer "default" wrapper).
func ParseNeighbors(b []byte) ([]Neighbor, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse neighbor json: %w", err)
	}
	nb, ok := raw["neighbors"]
	if !ok {
		if def, ok := raw["default"]; ok {
			var inner map[string]json.RawMessage
			if json.Unmarshal(def, &inner) == nil {
				nb = inner["neighbors"]
			}
		}
	}
	if nb == nil {
		return []Neighbor{}, nil
	}
	var byID map[string][]struct {
		Priority      int    `json:"priority"`
		State         string `json:"state"`
		Converged     string `json:"converged"`
		Address       string `json:"address"`
		IfaceAddress  string `json:"ifaceAddress"`
		IfaceName     string `json:"ifaceName"`
		DeadTimeMsecs int    `json:"routerDeadIntervalTimerDueMsec"`
		UpTimeInMsec  int    `json:"upTimeInMsec"`
		NbrPriority   int    `json:"nbrPriority"`
		NbrState      string `json:"nbrState"`
	}
	if err := json.Unmarshal(nb, &byID); err != nil {
		return nil, fmt.Errorf("parse neighbors: %w", err)
	}
	out := []Neighbor{}
	for id, list := range byID {
		for _, n := range list {
			state := n.State
			if state == "" {
				state = n.NbrState
			}
			if n.Converged != "" && !strings.Contains(state, "/") {
				state = n.Converged
			}
			addr := n.Address
			if addr == "" {
				addr = n.IfaceAddress
			}
			prio := n.Priority
			if prio == 0 {
				prio = n.NbrPriority
			}
			out = append(out, Neighbor{RouterID: id, Address: addr, Interface: n.IfaceName, State: state, Priority: prio, DeadTime: fmt.Sprintf("%.1fs", float64(n.DeadTimeMsecs)/1000)})
		}
	}
	return out, nil
}

// Health implements driver.Driver.
func (d *Driver) Health(ctx context.Context) model.ComponentHealth {
	h := model.ComponentHealth{Name: model.ComponentFRR, Status: model.StatusUnknown, LastError: d.lastErr}
	if !d.lastApply.IsZero() {
		t := d.lastApply
		h.LastApply = &t
	}
	if d.mode == model.ModeA || d.mode == "" {
		h.Status = model.StatusDisabled
		return h
	}
	if !d.installed() {
		h.Status = model.StatusDown
		if h.LastError == "" {
			h.LastError = "frr not installed"
		}
		return h
	}
	st, err := d.Systemd.State(ctx, d.Unit)
	switch {
	case err != nil:
		h.LastError = err.Error()
	case !st.Active():
		h.Status = model.StatusDown
	case d.lastErr != "" || d.Routes.Health(ctx).Status == model.StatusDegraded:
		h.Status = model.StatusDegraded
		if h.LastError == "" {
			h.LastError = d.Routes.Health(ctx).LastError
		}
	default:
		h.Status = model.StatusActive
	}
	return h
}
