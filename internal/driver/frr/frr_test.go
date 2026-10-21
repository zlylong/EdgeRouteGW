package frr

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/driver/route"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/routes"
)

func kinds(p driver.Plan) string {
	var k []string
	for _, a := range p.Actions {
		k = append(k, string(a.Kind))
	}
	return strings.Join(k, ",")
}

func newDriver(t *testing.T) (*Driver, *systemd.Fake, *route.Fake) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "frr")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "daemons"), []byte("bgpd=no\nospfd=no\nzebra=yes\n"), 0o644)
	sd := systemd.NewFake()
	sd.SetActive("frr.service", false)
	k := route.NewFake()
	rd := &route.Driver{Kernel: k, Batch: 100}
	d := New(&execx.Fake{}, sd, rd, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.ConfDir = dir
	return d, sd, k
}

func TestModeCBringsUpFRR(t *testing.T) {
	d, sd, k := newDriver(t)
	ctx := context.Background()
	next := &render.Artifacts{Mode: model.ModeC, FRR: []byte("router ospf\n redistribute table 100\n"), Routes: []routes.Route{{Prefix: netip.MustParsePrefix("8.8.8.0/24")}}}
	plan, err := d.Plan(ctx, nil, next)
	if err != nil {
		t.Fatal(err)
	}
	if kinds(plan) != "write,write,start,exec" {
		t.Fatalf("plan %s %+v", kinds(plan), plan.Actions)
	}
	if err := d.Apply(ctx, plan, next); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(d.daemonsPath()); !strings.Contains(string(b), "ospfd=yes") || strings.Contains(string(b), "ospfd=no") {
		t.Fatalf("daemons %q", b)
	}
	if b, _ := os.ReadFile(d.confPath()); string(b) != string(next.FRR) {
		t.Fatal("frr.conf not written")
	}
	if st, _ := sd.State(ctx, "frr.service"); !st.Active() {
		t.Fatal("frr not started")
	}
	if have, _ := k.List(render.RouteTable, render.RouteProtocol); len(have) != 1 {
		t.Fatalf("routes %v", have)
	}
	if d.Health(ctx).Status != "active" {
		t.Fatalf("health %+v", d.Health(ctx))
	}
	// Unchanged -> noop.
	plan, _ = d.Plan(ctx, next, next)
	if plan.Changes() {
		t.Fatalf("expected noop, got %+v", plan.Actions)
	}
	// Config change -> restart; route change -> exec.
	next2 := &render.Artifacts{Mode: model.ModeC, FRR: []byte("router ospf\n ospf router-id 1.1.1.1\n redistribute table 100\n"), Routes: next.Routes}
	plan, _ = d.Plan(ctx, next, next2)
	if kinds(plan) != "write,restart" {
		t.Fatalf("plan %s", kinds(plan))
	}
	sd.Restarts = map[string]int{}
	if err := d.Apply(ctx, plan, next2); err != nil {
		t.Fatal(err)
	}
	if sd.Restarts["frr.service"] != 1 {
		t.Fatal("expected restart")
	}

	// Back to Mode A: routes withdrawn, FRR stopped, health disabled.
	a := &render.Artifacts{Mode: model.ModeA}
	plan, _ = d.Plan(ctx, next2, a)
	if kinds(plan) != "exec,exec" {
		t.Fatalf("mode A plan %s %+v", kinds(plan), plan.Actions)
	}
	if err := d.Apply(ctx, plan, a); err != nil {
		t.Fatal(err)
	}
	if have, _ := k.List(render.RouteTable, render.RouteProtocol); len(have) != 0 {
		t.Fatal("routes not withdrawn")
	}
	if st, _ := sd.State(ctx, "frr.service"); st.Active() {
		t.Fatal("frr still running in Mode A")
	}
	if d.Health(ctx).Status != "disabled" {
		t.Fatalf("health %+v", d.Health(ctx))
	}
}

func TestNotInstalledIsSkipped(t *testing.T) {
	d, _, _ := newDriver(t)
	d.ConfDir = filepath.Join(t.TempDir(), "missing")
	plan, err := d.Plan(context.Background(), nil, &render.Artifacts{Mode: model.ModeB, FRR: []byte("x")})
	if err != nil || kinds(plan) != "skipped" {
		t.Fatalf("plan %s err %v", kinds(plan), err)
	}
}

func TestParseNeighbors(t *testing.T) {
	js := `{"neighbors":{"192.168.1.1":[{"priority":1,"state":"Full/DR","address":"192.168.1.1","ifaceName":"eth0:192.168.1.2","routerDeadIntervalTimerDueMsec":35000}]}}`
	n, err := ParseNeighbors([]byte(js))
	if err != nil || len(n) != 1 || n[0].RouterID != "192.168.1.1" || n[0].State != "Full/DR" || n[0].Interface != "eth0:192.168.1.2" || n[0].DeadTime != "35.0s" {
		t.Fatalf("%v %+v", err, n)
	}
	n, err = ParseNeighbors([]byte(`{"default":{"neighbors":{"10.0.0.1":[{"nbrState":"Full/Backup","ifaceAddress":"10.0.0.1","ifaceName":"lan0","nbrPriority":1}]}}}`))
	if err != nil || len(n) != 1 || n[0].State != "Full/Backup" || n[0].Address != "10.0.0.1" {
		t.Fatalf("%v %+v", err, n)
	}
	if n, _ := ParseNeighbors([]byte(`{}`)); len(n) != 0 {
		t.Fatal("empty")
	}
}

func TestHealthAfterNoopPlanReflectsMode(t *testing.T) {
	d, sd, _ := newDriver(t)
	sd.Units["frr.service"] = systemd.UnitState{ActiveState: "active", SubState: "running"}
	next := &render.Artifacts{Mode: model.ModeC, FRR: []byte("router ospf\n redistribute table 200\n")}
	if _, err := d.Plan(context.Background(), next, next); err != nil {
		t.Fatal(err)
	}
	if h := d.Health(context.Background()); h.Status == model.StatusDisabled {
		t.Fatalf("health after plan in Mode C must not be disabled: %+v", h)
	}
}
