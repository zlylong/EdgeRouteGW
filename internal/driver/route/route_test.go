package route

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/routes"
)

func arts(ps ...string) *render.Artifacts {
	a := &render.Artifacts{}
	for _, p := range ps {
		a.Routes = append(a.Routes, routes.Route{Prefix: netip.MustParsePrefix(p)})
	}
	return a
}

func TestDiffAndBatches(t *testing.T) {
	k := NewFake()
	_ = k.Add(render.RouteTable, render.RouteProtocol, []netip.Prefix{netip.MustParsePrefix("1.1.1.0/24"), netip.MustParsePrefix("9.9.9.9/32")})
	var sleeps int
	d := &Driver{Kernel: k, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Batch: 2, Interval: time.Second,
		Sleep: func(context.Context, time.Duration) error { sleeps++; return nil }}
	ctx := context.Background()
	next := arts("1.1.1.0/24", "2.2.2.0/24", "3.3.3.0/24", "4.4.4.0/24", "5.5.5.0/24")
	plan, err := d.Plan(ctx, nil, next)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Changes() || plan.Actions[0].Detail != "routes +4 -1 (table 100)" {
		t.Fatalf("plan %+v", plan.Actions)
	}
	if err := d.Apply(ctx, plan, next); err != nil {
		t.Fatal(err)
	}
	have, _ := k.List(render.RouteTable, render.RouteProtocol)
	if len(have) != 5 || have[0].String() != "1.1.1.0/24" || have[4].String() != "5.5.5.0/24" {
		t.Fatalf("kernel %v", have)
	}
	if k.Dels != 1 || k.Adds != 6 || sleeps != 1 {
		t.Fatalf("dels=%d adds=%d sleeps=%d", k.Dels, k.Adds, sleeps)
	}
	plan, _ = d.Plan(ctx, next, next)
	if plan.Changes() {
		t.Fatalf("expected noop, got %+v", plan.Actions)
	}
	if d.Health(ctx).Status != "active" || d.Installed() != 5 {
		t.Fatalf("health %+v installed %d", d.Health(ctx), d.Installed())
	}
	// Mode A (no routes) withdraws everything.
	plan, _ = d.Plan(ctx, next, &render.Artifacts{})
	if err := d.Apply(ctx, plan, &render.Artifacts{}); err != nil {
		t.Fatal(err)
	}
	if have, _ := k.List(render.RouteTable, render.RouteProtocol); len(have) != 0 {
		t.Fatalf("routes not withdrawn: %v", have)
	}
}
