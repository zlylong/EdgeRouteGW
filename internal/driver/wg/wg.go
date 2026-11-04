// Package wg maintains kernel WireGuard interfaces for WireGuard nodes: one
// interface per node, a routing table with a default route through it, and
// an "ip rule fwmark <mark> lookup <table>" so Xray's marked freedom
// outbound uses the tunnel.
package wg

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// PeerState is what Health reads back.
type PeerState struct {
	LastHandshake time.Time
	RxBytes       int64
	TxBytes       int64
}

// Backend is the kernel interface; Netlink is production, Fake is for tests.
type Backend interface {
	// Ensure creates or updates the interface: link, keys, peers, addresses,
	// MTU, up, the table's default route and the fwmark rule.
	Ensure(iface render.WGIface) error
	// Verify lists what is missing from the kernel for an interface that
	// Ensure already set up: the link, the fwmark rule, the table's default
	// route. Other agents remove these (systemd-networkd drops foreign ip
	// rules when it restarts), so the driver re-runs Ensure when the list
	// is not empty.
	Verify(iface render.WGIface) ([]string, error)
	// Remove deletes the interface and its rule/table.
	Remove(name string, table, mark int) error
	// List returns the names of interfaces with the given prefix.
	List(prefix string) ([]string, error)
	// Peers reports peer state for an interface.
	Peers(name string) ([]PeerState, error)
}

// Driver implements driver.Driver.
type Driver struct {
	Backend Backend
	Log     *slog.Logger

	lastErr   string
	lastApply time.Time
	wanted    []render.WGIface
}

// New wires the production driver.
func New(b Backend, log *slog.Logger) *Driver { return &Driver{Backend: b, Log: log} }

// Component implements driver.Driver.
func (d *Driver) Component() model.Component { return model.ComponentWG }

type planState struct {
	add, update, remove []render.WGIface
	repair              []render.WGIface
	repairNotes         []string
	removeNames         []string
}

// Plan implements driver.Driver.
func (d *Driver) Plan(ctx context.Context, prev, next *render.Artifacts) (driver.Plan, error) {
	plan := driver.Plan{Component: model.ComponentWG}
	st := &planState{}
	plan.State = st
	wantByName := map[string]render.WGIface{}
	for _, w := range next.WG {
		wantByName[w.Name] = w
	}
	// What exists: from prev when we applied it, else from the kernel.
	have := map[string]render.WGIface{}
	if prev != nil {
		for _, w := range prev.WG {
			have[w.Name] = w
		}
	} else {
		names, err := d.Backend.List(render.WGIfacePrefix)
		if err != nil {
			return plan, err
		}
		for _, n := range names {
			have[n] = render.WGIface{Name: n} // unknown config: re-ensure
		}
	}
	for name, w := range wantByName {
		if old, ok := have[name]; !ok {
			st.add = append(st.add, w)
		} else if prev == nil || !sameIface(old, w) {
			st.update = append(st.update, w)
		} else if missing := d.missing(w); len(missing) > 0 {
			st.repair = append(st.repair, w)
			st.repairNotes = append(st.repairNotes, w.Name+": "+strings.Join(missing, ", "))
		}
	}
	for name, old := range have {
		if _, ok := wantByName[name]; !ok {
			st.remove = append(st.remove, old)
			st.removeNames = append(st.removeNames, name)
		}
	}
	sort.Slice(st.add, func(i, j int) bool { return st.add[i].Name < st.add[j].Name })
	sort.Slice(st.update, func(i, j int) bool { return st.update[i].Name < st.update[j].Name })
	sort.Slice(st.repair, func(i, j int) bool { return st.repair[i].Name < st.repair[j].Name })
	sort.Strings(st.repairNotes)
	sort.Strings(st.removeNames)
	if len(st.add)+len(st.update)+len(st.remove) == 0 {
		if len(st.repair) > 0 {
			plan.Actions = []driver.Action{{Kind: driver.ActionRepair, Detail: "restore kernel path: " + strings.Join(st.repairNotes, "; ")}}
			plan.Diff = "~ " + strings.Join(st.repairNotes, "\n~ ")
			return plan, nil
		}
		detail := fmt.Sprintf("%d kernel interface(s) unchanged", len(next.WG))
		if len(next.WG) == 0 {
			detail = "no WireGuard nodes on the kernel path"
		}
		plan.Actions = []driver.Action{{Kind: driver.ActionNoop, Detail: detail}}
		return plan, nil
	}
	plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionExec, Detail: fmt.Sprintf("wireguard interfaces +%d ~%d -%d", len(st.add), len(st.update), len(st.remove))})
	if len(st.repair) > 0 {
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionRepair, Detail: "restore kernel path: " + strings.Join(st.repairNotes, "; ")})
	}
	var b strings.Builder
	for _, w := range st.add {
		fmt.Fprintf(&b, "+ %s (node %s) table %d mark %#x\n", w.Name, w.NodeID, w.Table, w.Mark)
	}
	for _, w := range st.update {
		fmt.Fprintf(&b, "~ %s (node %s)\n", w.Name, w.NodeID)
	}
	for _, n := range st.removeNames {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	for _, n := range st.repairNotes {
		fmt.Fprintf(&b, "~ %s\n", n)
	}
	plan.Diff = b.String()
	return plan, nil
}

// missing runs the backend's Verify, logging (not failing) when the kernel
// cannot be queried.
func (d *Driver) missing(w render.WGIface) []string {
	missing, err := d.Backend.Verify(w)
	if err != nil {
		d.Log.Warn("wireguard kernel path check failed", "iface", w.Name, "err", err)
		return nil
	}
	return missing
}

func sameIface(a, b render.WGIface) bool {
	if a.NodeID != b.NodeID || a.PrivateKey != b.PrivateKey || a.MTU != b.MTU || a.Table != b.Table || a.Mark != b.Mark || len(a.Addresses) != len(b.Addresses) || len(a.Peers) != len(b.Peers) {
		return false
	}
	for i := range a.Addresses {
		if a.Addresses[i] != b.Addresses[i] {
			return false
		}
	}
	for i := range a.Peers {
		p, q := a.Peers[i], b.Peers[i]
		if p.PublicKey != q.PublicKey || p.PresharedKey != q.PresharedKey || p.Endpoint != q.Endpoint || p.Keepalive != q.Keepalive || strings.Join(p.AllowedIPs, ",") != strings.Join(q.AllowedIPs, ",") {
			return false
		}
	}
	return true
}

// Apply implements driver.Driver.
func (d *Driver) Apply(ctx context.Context, plan driver.Plan, next *render.Artifacts) error {
	d.lastApply = time.Now()
	d.wanted = next.WG
	st, _ := plan.State.(*planState)
	if st == nil {
		return nil
	}
	var firstErr error
	for _, old := range st.remove {
		if err := d.Backend.Remove(old.Name, old.Table, old.Mark); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove %s: %w", old.Name, err)
		}
	}
	for _, w := range append(st.add, st.update...) {
		if err := d.Backend.Ensure(w); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("configure %s: %w", w.Name, err)
		}
	}
	if len(st.repair) > 0 {
		d.Log.Warn("wireguard policy routing removed externally; restoring (a systemd-networkd restart drops foreign ip rules unless ManageForeignRoutingPolicyRules=no)", "missing", strings.Join(st.repairNotes, "; "))
	}
	for _, w := range st.repair {
		if err := d.Backend.Ensure(w); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("restore %s: %w", w.Name, err)
		}
	}
	if firstErr != nil {
		d.lastErr = firstErr.Error()
		return firstErr
	}
	d.lastErr = ""
	return nil
}

// HandshakeGrace is how long a peer may go without a handshake before the
// interface counts as down (a persistent keepalive refreshes every 25s;
// WireGuard rekeys at least every 2 minutes under traffic).
const HandshakeGrace = 3 * time.Minute

// Health implements driver.Driver.
func (d *Driver) Health(ctx context.Context) model.ComponentHealth {
	h := model.ComponentHealth{Name: model.ComponentWG, Status: model.StatusUnknown, LastError: d.lastErr}
	if !d.lastApply.IsZero() {
		t := d.lastApply
		h.LastApply = &t
	}
	if len(d.wanted) == 0 {
		h.Status = model.StatusDisabled
		return h
	}
	if d.lastErr != "" {
		h.Status = model.StatusDegraded
		return h
	}
	var stale, incomplete []string
	for _, w := range d.wanted {
		if missing := d.missing(w); len(missing) > 0 {
			incomplete = append(incomplete, w.Name+": "+strings.Join(missing, ", "))
		}
		peers, err := d.Backend.Peers(w.Name)
		if err != nil {
			h.Status = model.StatusDown
			h.LastError = fmt.Sprintf("%s: %v", w.Name, err)
			return h
		}
		for _, p := range peers {
			if p.LastHandshake.IsZero() || time.Since(p.LastHandshake) > HandshakeGrace {
				stale = append(stale, w.Name)
				break
			}
		}
	}
	if len(incomplete) > 0 {
		h.Status = model.StatusDegraded
		h.LastError = "kernel path incomplete (restored on the next pass): " + strings.Join(incomplete, "; ")
		return h
	}
	if len(stale) > 0 {
		h.Status = model.StatusDegraded
		h.LastError = "no recent handshake on " + strings.Join(stale, ", ") + " (endpoint unreachable or keys mismatch)"
		return h
	}
	h.Status = model.StatusActive
	return h
}
