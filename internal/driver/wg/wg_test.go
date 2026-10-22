package wg

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

func iface(id string, table int) render.WGIface {
	return render.WGIface{NodeID: id, Name: render.WGIfaceName(id), PrivateKey: "k", Addresses: []string{"10.0.0.2/32"}, MTU: 1420,
		Peers: []render.WGPeer{{PublicKey: "p", Endpoint: "1.2.3.4:51820", AllowedIPs: []string{"0.0.0.0/0"}, Keepalive: 25}}, Table: table, Mark: 0x100 + table - 300}
}

func TestPlanApplyLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	d := New(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := iface("a", 300)
	b := iface("b", 301)

	// First apply after start: nothing exists yet.
	next := &render.Artifacts{WG: []render.WGIface{a, b}}
	plan, err := d.Plan(ctx, nil, next)
	if err != nil || !plan.Changes() {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if err := d.Apply(ctx, plan, next); err != nil {
		t.Fatal(err)
	}
	if len(f.Ifaces) != 2 {
		t.Fatalf("ifaces %v", f.Calls)
	}
	// Same again: noop.
	if plan, _ = d.Plan(ctx, next, next); plan.Changes() {
		t.Fatalf("expected noop, got %+v", plan)
	}
	// Change b's endpoint, drop a: one update, one remove.
	b2 := b
	b2.Peers = []render.WGPeer{{PublicKey: "p", Endpoint: "5.6.7.8:51820", AllowedIPs: []string{"0.0.0.0/0"}, Keepalive: 25}}
	next2 := &render.Artifacts{WG: []render.WGIface{b2}}
	plan, _ = d.Plan(ctx, next, next2)
	if !strings.Contains(plan.Actions[0].Detail, "+0 ~1 -1") {
		t.Fatalf("plan %+v", plan.Actions)
	}
	if err := d.Apply(ctx, plan, next2); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Ifaces[a.Name]; ok {
		t.Fatal("a should be removed")
	}
	if f.Ifaces[b.Name].Peers[0].Endpoint != "5.6.7.8:51820" {
		t.Fatal("b not updated")
	}
	// Restart (prev nil) with a stale kernel interface: it is removed.
	f.Ifaces["ergw-deadbeef"] = render.WGIface{Name: "ergw-deadbeef"}
	plan, _ = d.Plan(ctx, nil, next2)
	if !strings.Contains(plan.Diff, "- ergw-deadbeef") {
		t.Fatalf("stale interface not planned for removal: %q", plan.Diff)
	}
	_ = d.Apply(ctx, plan, next2)
	if _, ok := f.Ifaces["ergw-deadbeef"]; ok {
		t.Fatal("stale interface survived")
	}
	// Health: no handshake yet -> degraded; recent -> active; none wanted -> disabled.
	if h := d.Health(ctx); h.Status != model.StatusDegraded {
		t.Fatalf("health %+v", h)
	}
	f.Handshake[b.Name] = time.Now()
	if h := d.Health(ctx); h.Status != model.StatusActive {
		t.Fatalf("health %+v", h)
	}
	empty := &render.Artifacts{}
	plan, _ = d.Plan(ctx, next2, empty)
	_ = d.Apply(ctx, plan, empty)
	if h := d.Health(ctx); h.Status != model.StatusDisabled {
		t.Fatalf("health %+v", h)
	}
}

func TestApplyReportsBackendFailure(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	a := iface("a", 300)
	f.FailOn = a.Name
	d := New(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	next := &render.Artifacts{WG: []render.WGIface{a}}
	plan, _ := d.Plan(ctx, nil, next)
	if err := d.Apply(ctx, plan, next); err == nil {
		t.Fatal("expected error")
	}
	if h := d.Health(ctx); h.Status != model.StatusDegraded || h.LastError == "" {
		t.Fatalf("health %+v", h)
	}
}
