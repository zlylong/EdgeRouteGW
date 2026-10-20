// Package nft drives nftables and the TPROXY policy route. The ruleset file
// replaces only the gateway's own table, atomically, so a reload never leaves
// a window without rules and never touches other tables on the host.
package nft

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// Driver implements driver.Driver for nftables + ip rule/route.
type Driver struct {
	Paths paths.Paths
	Run   execx.Runner
	Log   *slog.Logger
	// NftBinary and IPBinary default to "nft" and "ip".
	NftBinary, IPBinary string

	lastErr   string
	lastApply time.Time
	// applied is set once this process has loaded the ruleset; before that,
	// Plan compares with the live ruleset.
	applied bool
}

// New builds a driver.
func New(p paths.Paths, run execx.Runner, log *slog.Logger) *Driver {
	return &Driver{Paths: p, Run: run, Log: log, NftBinary: "nft", IPBinary: "ip"}
}

// Component implements driver.Driver.
func (d *Driver) Component() model.Component { return model.ComponentNftables }

// Plan implements driver.Driver.
func (d *Driver) Plan(ctx context.Context, prev, next *render.Artifacts) (driver.Plan, error) {
	plan := driver.Plan{Component: model.ComponentNftables}
	onDisk, _ := os.ReadFile(d.Paths.NftRuleset())
	fileChanged := string(onDisk) != string(next.Nft)
	if fileChanged {
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionWrite, Detail: d.Paths.NftRuleset()})
		plan.Diff = driver.LineDiff(onDisk, next.Nft, 100)
	}
	// Reload when the content changed, when this process has not applied yet
	// (after a restart the kernel may hold an older ruleset or none), or when
	// the table is missing from the live ruleset.
	need := fileChanged || !d.applied
	if !need {
		if live, err := d.Run.Run(ctx, d.NftBinary, "list", "tables", "inet"); err != nil || !strings.Contains(string(live.Output), "table inet "+render.NftTable) {
			need = true
		}
	}
	if need {
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionExec, Detail: "nft -f (atomic table replace)"})
	}
	if !d.applied {
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionExec, Detail: "ensure ip rule fwmark lookup + local route"})
	}
	if len(plan.Actions) == 0 {
		plan.Actions = []driver.Action{{Kind: driver.ActionNoop, Detail: "ruleset unchanged and loaded"}}
	}
	return plan, nil
}

// Apply implements driver.Driver.
func (d *Driver) Apply(ctx context.Context, plan driver.Plan, next *render.Artifacts) error {
	d.lastApply = time.Now()
	err := d.apply(ctx, plan, next)
	if err != nil {
		d.lastErr = err.Error()
	} else {
		d.lastErr = ""
	}
	return err
}

func (d *Driver) apply(ctx context.Context, plan driver.Plan, next *render.Artifacts) error {
	for _, a := range plan.Actions {
		switch a.Kind {
		case driver.ActionWrite:
			if _, err := driver.WriteFileAtomic(d.Paths.NftRuleset(), next.Nft, 0o644); err != nil {
				return fmt.Errorf("write ruleset: %w", err)
			}
		case driver.ActionExec:
			if strings.HasPrefix(a.Detail, "nft -f") {
				// Make sure the file on disk is what we validate and load.
				if !driver.SameContent(d.Paths.NftRuleset(), next.Nft) {
					if _, err := driver.WriteFileAtomic(d.Paths.NftRuleset(), next.Nft, 0o644); err != nil {
						return err
					}
				}
				if res, err := d.Run.Run(ctx, d.NftBinary, "-c", "-f", d.Paths.NftRuleset()); err != nil {
					return fmt.Errorf("ruleset rejected by nft -c: %s", strings.TrimSpace(string(res.Output)))
				}
				if res, err := d.Run.Run(ctx, d.NftBinary, "-f", d.Paths.NftRuleset()); err != nil {
					return fmt.Errorf("nft -f: %s", strings.TrimSpace(string(res.Output)))
				}
				d.applied = true
			} else {
				if err := d.ensurePolicyRouting(ctx); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ensurePolicyRouting installs "ip rule fwmark <mark> lookup <table>" and the
// "local default dev lo" route in that table for v4 and v6, idempotently.
// These make the kernel deliver TPROXY-marked packets to the local socket.
func (d *Driver) ensurePolicyRouting(ctx context.Context) error {
	mark := fmt.Sprintf("%d", render.TProxyMark)
	table := fmt.Sprintf("%d", render.TProxyRouteTbl)
	for _, fam := range []string{"-4", "-6"} {
		if fam == "-6" && ipv6Disabled() {
			continue
		}
		res, err := d.Run.Run(ctx, d.IPBinary, fam, "rule", "show")
		if err != nil {
			return fmt.Errorf("ip %s rule show: %w", fam, err)
		}
		if !strings.Contains(string(res.Output), "fwmark 0x"+mark+" lookup "+table) {
			if _, err := d.Run.Run(ctx, d.IPBinary, fam, "rule", "add", "fwmark", mark, "lookup", table); err != nil {
				return fmt.Errorf("ip %s rule add: %w", fam, err)
			}
		}
		res, err = d.Run.Run(ctx, d.IPBinary, fam, "route", "show", "table", table)
		if err != nil {
			// An empty table is reported as an error by some iproute2 versions.
			res.Output = nil
		}
		if !strings.Contains(string(res.Output), "local default dev lo") {
			if _, err := d.Run.Run(ctx, d.IPBinary, fam, "route", "replace", "local", "default", "dev", "lo", "table", table); err != nil {
				return fmt.Errorf("ip %s route replace: %w", fam, err)
			}
		}
	}
	return nil
}

func ipv6Disabled() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// Health implements driver.Driver.
func (d *Driver) Health(ctx context.Context) model.ComponentHealth {
	h := model.ComponentHealth{Name: model.ComponentNftables, Status: model.StatusUnknown, LastError: d.lastErr}
	if !d.lastApply.IsZero() {
		t := d.lastApply
		h.LastApply = &t
	}
	res, err := d.Run.Run(ctx, d.NftBinary, "list", "tables", "inet")
	switch {
	case err != nil:
		h.Status = model.StatusDown
		if h.LastError == "" {
			h.LastError = "nft not available"
		}
	case !strings.Contains(string(res.Output), "table inet "+render.NftTable):
		h.Status = model.StatusDown
		if h.LastError == "" {
			h.LastError = "table not loaded"
		}
	case d.lastErr != "":
		h.Status = model.StatusDegraded
	default:
		h.Status = model.StatusActive
	}
	return h
}
