package nft

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

func TestApplySequence(t *testing.T) {
	run := &execx.Fake{}
	loaded := false
	run.Handler = func(name string, args []string) (execx.Result, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "nft" && joined == "list tables inet":
			if loaded {
				return execx.Result{Output: []byte("table inet filter\ntable inet edgeroutegw\n")}, nil
			}
			return execx.Result{Output: []byte("table inet filter\n")}, nil
		case name == "nft" && strings.HasPrefix(joined, "-f "):
			loaded = true
		case name == "ip" && strings.Contains(joined, "rule show"):
			return execx.Result{Output: []byte("0:\tfrom all lookup local\n")}, nil
		case name == "ip" && strings.Contains(joined, "route show table"):
			return execx.Result{Output: []byte("")}, nil
		}
		return execx.Result{}, nil
	}
	d := New(paths.New(t.TempDir()), run, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	a := &render.Artifacts{Nft: []byte("table inet edgeroutegw {}\n")}

	plan, err := d.Plan(ctx, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Changes() {
		t.Fatal("first plan must change")
	}
	if err := d.Apply(ctx, plan, a); err != nil {
		t.Fatal(err)
	}
	if run.Count("nft", "-c", "-f") != 1 || run.Count("nft", "-f") != 1 {
		t.Fatalf("expected check then load, calls %+v", run.Calls)
	}
	if run.Count("ip", "-4", "rule", "add") != 1 || run.Count("ip", "-4", "route", "replace") != 1 {
		t.Fatalf("policy routing not installed: %+v", run.Calls)
	}
	if d.Health(ctx).Status != "active" {
		t.Fatalf("health %+v", d.Health(ctx))
	}

	// Second pass with the same ruleset: nothing to do.
	plan, _ = d.Plan(ctx, a, a)
	if plan.Changes() {
		t.Fatalf("expected noop, got %+v", plan.Actions)
	}

	// Table vanished externally -> reload even though the file is unchanged.
	loaded = false
	plan, _ = d.Plan(ctx, a, a)
	if !plan.Changes() {
		t.Fatal("missing table must trigger reload")
	}
}

func TestRejectedRulesetIsNotLoaded(t *testing.T) {
	run := &execx.Fake{}
	run.Handler = func(name string, args []string) (execx.Result, error) {
		if name == "nft" && len(args) > 0 && args[0] == "-c" {
			return execx.Result{Output: []byte("syntax error")}, context.DeadlineExceeded
		}
		return execx.Result{}, nil
	}
	d := New(paths.New(t.TempDir()), run, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := &render.Artifacts{Nft: []byte("bad")}
	plan, _ := d.Plan(context.Background(), nil, a)
	if err := d.Apply(context.Background(), plan, a); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err %v", err)
	}
	if run.Count("nft", "-f") != 0 {
		t.Fatal("must not load a ruleset that failed the check")
	}
}

func TestCountRulesToleratesNamedTableAndDedupes(t *testing.T) {
	out := "0:\tfrom all lookup local\n32764:\tfrom all fwmark 0x1 lookup tproxy\n32765:\tfrom all fwmark 0x1 lookup 100\n32766:\tfrom all lookup main\n"
	if n := countRules(out, "1", "100"); n != 2 {
		t.Fatalf("count %d", n)
	}
	if n := countRules("32765:\tfrom all fwmark 0x1/0xff lookup 100\n", "1", "100"); n != 1 {
		t.Fatalf("masked count %d", n)
	}
	if n := countRules("32765:\tfrom all fwmark 0x1 lookup 1000\n", "1", "100"); n != 0 {
		t.Fatalf("prefix match %d", n)
	}
	// Driver collapses the duplicates and adds nothing.
	dels := 0
	run := &execx.Fake{Handler: func(name string, args []string) (execx.Result, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "ip" && strings.Contains(joined, "-6"):
			return execx.Result{Output: []byte("0:\tfrom all lookup local\n32766:\tfrom all fwmark 0x1 lookup 100\n")}, nil
		case name == "ip" && strings.Contains(joined, "rule show"):
			return execx.Result{Output: []byte(out)}, nil
		case name == "ip" && strings.Contains(joined, "rule del"):
			dels++
		case name == "ip" && strings.Contains(joined, "route show table"):
			return execx.Result{Output: []byte("local default dev lo scope host\n")}, nil
		}
		return execx.Result{}, nil
	}}
	d := New(paths.New(t.TempDir()), run, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := d.ensurePolicyRouting(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dels != 1 || run.Count("ip", "-4", "rule", "add") != 0 || run.Count("ip", "-4", "route", "replace") != 0 {
		t.Fatalf("dels=%d calls=%+v", dels, run.Calls)
	}
}
