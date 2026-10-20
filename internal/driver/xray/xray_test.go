package xray

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

func newDriver(t *testing.T, active bool) (*Driver, *execx.Fake, *systemd.Fake) {
	t.Helper()
	p := paths.New(t.TempDir())
	p.RunDir = filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(filepath.Dir(p.XrayBinary()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.XrayBinary(), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := &execx.Fake{}
	sd := systemd.NewFake()
	sd.SetActive("edgeroutegw-xray.service", active)
	d := New(p, run, sd, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return d, run, sd
}

func arts(structural string, outbounds map[string]string, rules string) *render.Artifacts {
	obs := map[string]json.RawMessage{}
	for k, v := range outbounds {
		obs[k] = json.RawMessage(v)
	}
	full := map[string]any{"structural": structural, "outbounds": outbounds, "rules": rules}
	b, _ := json.Marshal(full)
	return &render.Artifacts{Xray: b, XrayOutbounds: obs, XrayRules: json.RawMessage(rules), XrayStructuralHash: structural}
}

func kinds(p driver.Plan) string {
	var k []string
	for _, a := range p.Actions {
		k = append(k, string(a.Kind))
	}
	return strings.Join(k, ",")
}

func TestFirstApplyWritesAndRestarts(t *testing.T) {
	d, run, sd := newDriver(t, true)
	ctx := context.Background()
	a := arts("s1", map[string]string{"node-a": `{"tag":"node-a"}`}, `[{"ruleTag":"x"}]`)
	plan, err := d.Plan(ctx, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	if kinds(plan) != "write,restart" {
		t.Fatalf("plan %s", kinds(plan))
	}
	if err := d.Apply(ctx, plan, a); err != nil {
		t.Fatal(err)
	}
	if run.Count(d.Paths.XrayBinary(), "-test") != 1 {
		t.Fatal("config must be validated with xray -test before writing")
	}
	if sd.Restarts[d.Unit] != 1 {
		t.Fatalf("restarts %v", sd.Restarts)
	}
	got, _ := os.ReadFile(d.Paths.XrayConfig())
	if string(got) != string(a.Xray) {
		t.Fatal("config not written")
	}
	st, _ := os.Stat(d.Paths.XrayConfig())
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config perm %o, want 600", st.Mode().Perm())
	}

	// Same artifacts again: nothing to do.
	plan, _ = d.Plan(ctx, a, a)
	if plan.Changes() {
		t.Fatalf("unchanged config must be noop, got %s", kinds(plan))
	}
	if d.Health(ctx).Status != "active" {
		t.Fatalf("health %+v", d.Health(ctx))
	}
}

func TestHotUpdateForOutboundAndRuleChanges(t *testing.T) {
	d, run, sd := newDriver(t, true)
	ctx := context.Background()
	prev := arts("s1", map[string]string{"node-a": `{"tag":"node-a","v":1}`, "node-b": `{"tag":"node-b"}`}, `[]`)
	plan, _ := d.Plan(ctx, nil, prev)
	if err := d.Apply(ctx, plan, prev); err != nil {
		t.Fatal(err)
	}
	run.Reset()
	sd.Restarts = map[string]int{}

	// node-a changed, node-b removed, node-c added; same structure.
	next := arts("s1", map[string]string{"node-a": `{"tag":"node-a","v":2}`, "node-c": `{"tag":"node-c"}`}, `[{"ruleTag":"r"}]`)
	plan, err := d.Plan(ctx, prev, next)
	if err != nil {
		t.Fatal(err)
	}
	if kinds(plan) != "write,hot" {
		t.Fatalf("plan %s (%+v)", kinds(plan), plan.Actions)
	}
	if err := d.Apply(ctx, plan, next); err != nil {
		t.Fatal(err)
	}
	if sd.Restarts[d.Unit] != 0 {
		t.Fatal("hot update must not restart")
	}
	var rmo, ado, adrules int
	for _, c := range run.Calls {
		if len(c.Args) >= 2 && c.Args[0] == "api" {
			switch c.Args[1] {
			case "rmo":
				rmo++
				joined := strings.Join(c.Args, " ")
				if !strings.Contains(joined, "node-a") || !strings.Contains(joined, "node-b") || strings.Contains(joined, "node-c") {
					t.Fatalf("rmo args %v", c.Args)
				}
			case "ado":
				ado++
			case "adrules":
				adrules++
			}
		}
	}
	if rmo != 1 || ado != 1 || adrules != 1 {
		t.Fatalf("rmo=%d ado=%d adrules=%d", rmo, ado, adrules)
	}
}

func TestStructuralChangeRestarts(t *testing.T) {
	d, _, sd := newDriver(t, true)
	ctx := context.Background()
	prev := arts("s1", map[string]string{"node-a": `{}`}, `[]`)
	plan, _ := d.Plan(ctx, nil, prev)
	_ = d.Apply(ctx, plan, prev)
	next := arts("s2", map[string]string{"node-a": `{}`}, `[]`)
	plan, _ = d.Plan(ctx, prev, next)
	if kinds(plan) != "write,restart" {
		t.Fatalf("plan %s", kinds(plan))
	}
	if err := d.Apply(ctx, plan, next); err != nil {
		t.Fatal(err)
	}
	if sd.Restarts[d.Unit] != 2 {
		t.Fatalf("restarts %v", sd.Restarts)
	}
}

func TestHotFailureFallsBackToRestart(t *testing.T) {
	d, run, sd := newDriver(t, true)
	ctx := context.Background()
	prev := arts("s1", map[string]string{"node-a": `{"v":1}`}, `[]`)
	plan, _ := d.Plan(ctx, nil, prev)
	_ = d.Apply(ctx, plan, prev)
	sd.Restarts = map[string]int{}
	run.Handler = func(name string, args []string) (execx.Result, error) {
		if len(args) > 1 && args[0] == "api" && args[1] == "adrules" {
			return execx.Result{Output: []byte("rpc error")}, errors.New("exit 1")
		}
		return execx.Result{}, nil
	}
	next := arts("s1", map[string]string{"node-a": `{"v":2}`}, `[]`)
	plan, _ = d.Plan(ctx, prev, next)
	if err := d.Apply(ctx, plan, next); err != nil {
		t.Fatalf("fallback should succeed: %v", err)
	}
	if sd.Restarts[d.Unit] != 1 {
		t.Fatal("expected restart fallback after hot failure")
	}
}

func TestInvalidConfigIsRejectedBeforeWrite(t *testing.T) {
	d, run, _ := newDriver(t, true)
	ctx := context.Background()
	run.Handler = func(name string, args []string) (execx.Result, error) {
		if len(args) > 0 && args[0] == "-test" {
			return execx.Result{Output: []byte("Failed to start: bad config")}, errors.New("exit 23")
		}
		return execx.Result{}, nil
	}
	a := arts("s1", nil, `[]`)
	plan, _ := d.Plan(ctx, nil, a)
	if err := d.Apply(ctx, plan, a); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected validation error, got %v", err)
	}
	if _, err := os.Stat(d.Paths.XrayConfig()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected config must not be written")
	}
	if d.Health(ctx).Status != "degraded" {
		t.Fatalf("health %+v", d.Health(ctx))
	}
}

func TestInactiveUnitIsStarted(t *testing.T) {
	d, _, sd := newDriver(t, false)
	ctx := context.Background()
	a := arts("s1", nil, `[]`)
	plan, _ := d.Plan(ctx, nil, a)
	if kinds(plan) != "write,start" {
		t.Fatalf("plan %s", kinds(plan))
	}
	if err := d.Apply(ctx, plan, a); err != nil {
		t.Fatal(err)
	}
	st, _ := sd.State(ctx, d.Unit)
	if !st.Active() {
		t.Fatal("unit should be active after start")
	}
}

func TestMissingBinaryIsSkipped(t *testing.T) {
	d, _, _ := newDriver(t, false)
	_ = os.Remove(d.Paths.XrayBinary())
	a := arts("s1", nil, `[]`)
	plan, _ := d.Plan(context.Background(), nil, a)
	if kinds(plan) != "write,skipped" {
		t.Fatalf("plan %s", kinds(plan))
	}
	if err := d.Apply(context.Background(), plan, a); err != nil {
		t.Fatal(err)
	}
	if d.Health(context.Background()).Status != "down" {
		t.Fatal("missing binary should report down")
	}
}
