package mosdns

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

func newDriver(t *testing.T) (*Driver, *systemd.Fake, *[]time.Duration) {
	t.Helper()
	p := paths.New(t.TempDir())
	p.RunDir = filepath.Join(t.TempDir(), "run")
	_ = os.MkdirAll(filepath.Dir(p.MosdnsBinary()), 0o755)
	_ = os.WriteFile(p.MosdnsBinary(), []byte("x"), 0o755)
	sd := systemd.NewFake("edgeroutegw-mosdns.service")
	d := New(p, sd, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var sleeps []time.Duration
	d.Sleep = func(_ context.Context, w time.Duration) error { sleeps = append(sleeps, w); return nil }
	return d, sd, &sleeps
}

func has(p driver.Plan, k driver.ActionKind) bool {
	for _, a := range p.Actions {
		if a.Kind == k {
			return true
		}
	}
	return false
}

func TestWriteRestartAndRateLimit(t *testing.T) {
	d, sd, sleeps := newDriver(t)
	ctx := context.Background()
	a := &render.Artifacts{MosdnsConfig: []byte("cfg1"), MosdnsProxyDomains: []byte("a.com\n")}
	plan, err := d.Plan(ctx, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	if !has(plan, driver.ActionWrite) || !has(plan, driver.ActionRestart) {
		t.Fatalf("plan %+v", plan.Actions)
	}
	if err := d.Apply(ctx, plan, a); err != nil {
		t.Fatal(err)
	}
	if sd.Restarts[d.Unit] != 1 {
		t.Fatalf("restarts %v", sd.Restarts)
	}
	// Placeholder CN list created because no geodata was rendered.
	if _, err := os.Stat(d.Paths.MosdnsCNList()); err != nil {
		t.Fatal("cn placeholder missing")
	}
	// Unchanged -> noop.
	plan, _ = d.Plan(ctx, a, a)
	if plan.Changes() {
		t.Fatalf("expected noop, got %+v", plan.Actions)
	}
	// Change immediately after: restart is delayed by the remaining gap.
	b := &render.Artifacts{MosdnsConfig: []byte("cfg2"), MosdnsProxyDomains: []byte("a.com\n"), MosdnsCNDomains: []byte("domain:cn\n")}
	plan, _ = d.Plan(ctx, a, b)
	if err := d.Apply(ctx, plan, b); err != nil {
		t.Fatal(err)
	}
	if sd.Restarts[d.Unit] != 2 {
		t.Fatal("second restart missing")
	}
	if len(*sleeps) != 1 || (*sleeps)[0] <= 0 || (*sleeps)[0] > d.MinRestartGap {
		t.Fatalf("expected one rate-limit wait, got %v", *sleeps)
	}
	cn, _ := os.ReadFile(d.Paths.MosdnsCNList())
	if string(cn) != "domain:cn\n" {
		t.Fatalf("cn list %q", cn)
	}
}

func TestInactiveStartsWithoutRestart(t *testing.T) {
	d, sd, _ := newDriver(t)
	sd.SetActive(d.Unit, false)
	ctx := context.Background()
	a := &render.Artifacts{MosdnsConfig: []byte("cfg"), MosdnsProxyDomains: []byte("")}
	plan, _ := d.Plan(ctx, nil, a)
	if !has(plan, driver.ActionStart) || has(plan, driver.ActionRestart) {
		t.Fatalf("plan %+v", plan.Actions)
	}
	if err := d.Apply(ctx, plan, a); err != nil {
		t.Fatal(err)
	}
	if d.Health(ctx).Status != "active" {
		t.Fatalf("health %+v", d.Health(ctx))
	}
}
