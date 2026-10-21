// Package route drives the kernel routing table that FRR redistributes: it
// diffs the rendered prefix set against what table 100 holds and applies
// adds and deletes in batches with netlink.
package route

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// Driver implements driver.Driver for published routes. Its component is
// reported under "frr" together with the FRR driver's health, since from the
// operator's point of view they are one thing: what OSPF announces.
type Driver struct {
	Kernel Kernel
	Log    *slog.Logger
	// Batch and Interval pace large pushes (geoip expansions reach hundreds
	// of thousands of prefixes): at most Batch operations per Interval.
	Batch    int
	Interval time.Duration
	Sleep    func(ctx context.Context, d time.Duration) error

	lastErr   string
	lastApply time.Time
	cleaned   bool
	// counts for Health
	installed int
}

// New builds a driver on the real kernel.
func New(log *slog.Logger) *Driver {
	return &Driver{Kernel: &Netlink{}, Log: log, Batch: 500, Interval: 2 * time.Second, Sleep: sleepCtx}
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
func (d *Driver) Component() model.Component { return model.ComponentFRR }

type planState struct {
	add, del []netip.Prefix
}

// Plan implements driver.Driver.
func (d *Driver) Plan(_ context.Context, _, next *render.Artifacts) (driver.Plan, error) {
	plan := driver.Plan{Component: model.ComponentFRR}
	want := map[netip.Prefix]bool{}
	for _, r := range next.Routes {
		want[r.Prefix] = true
	}
	have, err := d.Kernel.List(render.RouteTable, render.RouteProtocol)
	if err != nil {
		return plan, fmt.Errorf("list kernel routes: %w", err)
	}
	haveSet := map[netip.Prefix]bool{}
	st := &planState{}
	for _, p := range have {
		haveSet[p] = true
		if !want[p] {
			st.del = append(st.del, p)
		}
	}
	for _, r := range next.Routes {
		if !haveSet[r.Prefix] {
			st.add = append(st.add, r.Prefix)
		}
	}
	sortPrefixes(st.add)
	sortPrefixes(st.del)
	if len(st.add) == 0 && len(st.del) == 0 {
		plan.Actions = []driver.Action{{Kind: driver.ActionNoop, Detail: fmt.Sprintf("%d route(s) in table %d, unchanged", len(have), render.RouteTable)}}
		return plan, nil
	}
	plan.Actions = []driver.Action{{Kind: driver.ActionExec, Detail: fmt.Sprintf("routes +%d -%d (table %d)", len(st.add), len(st.del), render.RouteTable)}}
	plan.Diff = routeDiff(st.add, st.del, 60)
	plan.State = st
	return plan, nil
}

func routeDiff(add, del []netip.Prefix, max int) string {
	var lines []string
	for _, p := range del {
		lines = append(lines, "- "+p.String())
	}
	for _, p := range add {
		lines = append(lines, "+ "+p.String())
	}
	if len(lines) > max {
		rest := len(lines) - max
		lines = append(lines[:max], fmt.Sprintf("… %d more", rest))
	}
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// Apply implements driver.Driver.
// LegacyTables held published routes in earlier 2.0 builds; whatever is
// still there (our protocol only) is removed on the first apply.
var LegacyTables = []int{100}

func (d *Driver) Apply(ctx context.Context, plan driver.Plan, _ *render.Artifacts) error {
	d.lastApply = time.Now()
	if !d.cleaned {
		d.cleaned = true
		for _, t := range LegacyTables {
			if t == render.RouteTable {
				continue
			}
			if old, err := d.Kernel.List(t, render.RouteProtocol); err == nil && len(old) > 0 {
				if err := d.Kernel.Del(t, render.RouteProtocol, old); err != nil {
					d.Log.Warn("legacy route table cleanup failed", "table", t, "err", err)
				} else {
					d.Log.Info("removed routes left in legacy table", "table", t, "count", len(old))
				}
			}
		}
	}
	st, _ := plan.State.(*planState)
	if st == nil {
		return nil
	}
	err := d.apply(ctx, st)
	if err != nil {
		d.lastErr = err.Error()
	} else {
		d.lastErr = ""
	}
	return err
}

func (d *Driver) apply(ctx context.Context, st *planState) error {
	batch := d.Batch
	if batch <= 0 {
		batch = 500
	}
	// Deletes first so a shrinking set never has a window with both the
	// broad and the narrow prefix present.
	if err := d.paced(ctx, st.del, batch, func(ps []netip.Prefix) error { return d.Kernel.Del(render.RouteTable, render.RouteProtocol, ps) }); err != nil {
		return fmt.Errorf("delete routes: %w", err)
	}
	if err := d.paced(ctx, st.add, batch, func(ps []netip.Prefix) error { return d.Kernel.Add(render.RouteTable, render.RouteProtocol, ps) }); err != nil {
		return fmt.Errorf("add routes: %w", err)
	}
	if have, err := d.Kernel.List(render.RouteTable, render.RouteProtocol); err == nil {
		d.installed = len(have)
	}
	return nil
}

func (d *Driver) paced(ctx context.Context, ps []netip.Prefix, batch int, fn func([]netip.Prefix) error) error {
	for i := 0; i < len(ps); i += batch {
		end := i + batch
		if end > len(ps) {
			end = len(ps)
		}
		if err := fn(ps[i:end]); err != nil {
			return err
		}
		if end < len(ps) && d.Interval > 0 && d.Sleep != nil {
			if err := d.Sleep(ctx, d.Interval); err != nil {
				return err
			}
		}
	}
	return nil
}

// Installed reports how many prefixes the last apply left in the table.
func (d *Driver) Installed() int { return d.installed }

// Health implements driver.Driver. The FRR driver merges this into the
// component's health; on its own it only reports apply errors.
func (d *Driver) Health(_ context.Context) model.ComponentHealth {
	h := model.ComponentHealth{Name: model.ComponentFRR, Status: model.StatusActive, LastError: d.lastErr}
	if d.lastErr != "" {
		h.Status = model.StatusDegraded
	}
	if !d.lastApply.IsZero() {
		t := d.lastApply
		h.LastApply = &t
	}
	return h
}
