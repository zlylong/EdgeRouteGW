// Package xray drives Xray-core: it keeps config.json on disk in sync with the
// rendered artifact and chooses between a hot update over the Xray API and a
// unit restart.
//
// Hot path: when only node outbounds or routing rules changed, the running
// instance is updated with "xray api rmo/ado/adrules" (the CLI from the
// installed binary, so client and server always agree on the protocol).
// Anything structural (inbounds, fakedns, balancers, log settings) restarts.
// A failed hot update falls back to a restart within the same Apply.
package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// Driver implements driver.Driver for Xray.
type Driver struct {
	Paths   paths.Paths
	Unit    string // systemd unit name, e.g. "edgeroutegw-xray.service"
	Run     execx.Runner
	Systemd systemd.Manager
	Log     *slog.Logger

	// lastHealth is updated by Apply/Health for /state.
	lastErr   string
	lastApply time.Time
}

// New builds a driver with the production unit name.
func New(p paths.Paths, run execx.Runner, sd systemd.Manager, log *slog.Logger) *Driver {
	return &Driver{Paths: p, Unit: "edgeroutegw-xray.service", Run: run, Systemd: sd, Log: log}
}

// Component implements driver.Driver.
func (d *Driver) Component() model.Component { return model.ComponentXray }

// Plan implements driver.Driver.
func (d *Driver) Plan(ctx context.Context, prev, next *render.Artifacts) (driver.Plan, error) {
	plan := driver.Plan{Component: model.ComponentXray}
	onDisk, _ := os.ReadFile(d.Paths.XrayConfig())
	fileChanged := string(onDisk) != string(next.Xray)
	if fileChanged {
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionWrite, Detail: d.Paths.XrayConfig()})
		plan.Diff = driver.LineDiff(onDisk, next.Xray, 200)
	}

	state, err := d.Systemd.State(ctx, d.Unit)
	if err != nil {
		return plan, fmt.Errorf("xray unit state: %w", err)
	}
	if !state.Active() {
		if d.binaryMissing() {
			plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionSkipped, Detail: "xray binary not installed"})
			return plan, nil
		}
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionStart, Detail: d.Unit})
		return plan, nil
	}
	if !fileChanged {
		if len(plan.Actions) == 0 {
			plan.Actions = []driver.Action{{Kind: driver.ActionNoop, Detail: "config unchanged, unit active"}}
		}
		return plan, nil
	}
	// The file changed and the unit is running: hot update if we know what the
	// running instance holds (prev) and only hot-updatable parts differ.
	if prev != nil && prev.XrayStructuralHash == next.XrayStructuralHash {
		add, remove := diffOutbounds(prev.XrayOutbounds, next.XrayOutbounds)
		detail := fmt.Sprintf("outbounds +%d -%d, rules %d", len(add), len(remove), countRules(next.XrayRules))
		plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionHot, Detail: detail})
		plan.State = prev.XrayOutbounds
		return plan, nil
	}
	reason := "first apply since start"
	if prev != nil {
		reason = "structural change (inbounds/dns/balancers)"
	}
	plan.Actions = append(plan.Actions, driver.Action{Kind: driver.ActionRestart, Detail: reason})
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
	prevOutbounds, _ := plan.State.(map[string]json.RawMessage)
	for _, a := range plan.Actions {
		switch a.Kind {
		case driver.ActionWrite:
			if !d.binaryMissing() {
				if err := d.validate(ctx, next.Xray); err != nil {
					return err
				}
			}
			// Without planner state (should not happen), fall back to what
			// the running instance loaded from disk.
			if prevOutbounds == nil {
				if old, err := os.ReadFile(d.Paths.XrayConfig()); err == nil {
					prevOutbounds = outboundsFromConfig(old)
				}
			}
			if err := os.MkdirAll(d.Paths.RunDir, 0o755); err != nil {
				return fmt.Errorf("create runtime dir: %w", err)
			}
			if _, err := driver.WriteFileAtomic(d.Paths.XrayConfig(), next.Xray, 0o600); err != nil {
				return fmt.Errorf("write xray config: %w", err)
			}
		case driver.ActionStart:
			if err := d.Systemd.Start(ctx, d.Unit); err != nil {
				return err
			}
		case driver.ActionRestart:
			if err := d.Systemd.Restart(ctx, d.Unit); err != nil {
				return err
			}
		case driver.ActionHot:
			if err := d.hotUpdate(ctx, prevOutbounds, next); err != nil {
				d.Log.Warn("xray hot update failed, restarting instead", "err", err)
				if rerr := d.Systemd.Restart(ctx, d.Unit); rerr != nil {
					return fmt.Errorf("hot update failed (%v) and restart failed: %w", err, rerr)
				}
			}
		}
	}
	return nil
}

// hotUpdate pushes outbound and rule changes into the running instance.
func (d *Driver) hotUpdate(ctx context.Context, prevOutbounds map[string]json.RawMessage, next *render.Artifacts) error {
	add, remove := diffOutbounds(prevOutbounds, next.XrayOutbounds)
	// Changed outbounds appear in both lists: remove the old, add the new.
	if len(remove) > 0 {
		args := append([]string{"api", "rmo", "-s", render.APIListenAddr}, remove...)
		if res, err := d.Run.Run(ctx, d.Paths.XrayBinary(), args...); err != nil {
			out := strings.ToLower(string(res.Output))
			if !strings.Contains(out, "not found") {
				return fmt.Errorf("rmo: %w", err)
			}
		}
	}
	if len(add) > 0 {
		obs := make([]json.RawMessage, 0, len(add))
		for _, tag := range add {
			obs = append(obs, next.XrayOutbounds[tag])
		}
		payload, _ := json.Marshal(map[string]any{"outbounds": obs})
		if err := d.runWithTempFile(ctx, "ado", payload); err != nil {
			return fmt.Errorf("ado: %w", err)
		}
	}
	// adrules replaces the whole rule list by default; include domainStrategy
	// so the payload is a complete routing object.
	payload, _ := json.Marshal(map[string]any{"routing": map[string]any{"domainStrategy": "IPIfNonMatch", "rules": next.XrayRules}})
	if err := d.runWithTempFile(ctx, "adrules", payload); err != nil {
		return fmt.Errorf("adrules: %w", err)
	}
	return nil
}

// runWithTempFile hands an owner-only temp file to "xray api <sub>". The
// payload carries credentials, so it never lives at a predictable path.
func (d *Driver) runWithTempFile(ctx context.Context, sub string, payload []byte) error {
	f, err := os.CreateTemp("", "ergw-xray-"+sub+"-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = d.Run.Run(ctx, d.Paths.XrayBinary(), "api", sub, "-s", render.APIListenAddr, name)
	return err
}

// validate runs "xray -test" against the candidate config. The access log
// directory must exist or Xray refuses the config outright (exit 23).
func (d *Driver) validate(ctx context.Context, cfg []byte) error {
	if err := os.MkdirAll(d.Paths.RunDir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp("", "ergw-xray-test-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(cfg); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if res, err := d.Run.Run(ctx, d.Paths.XrayBinary(), "-test", "-config", name); err != nil {
		return fmt.Errorf("xray rejected the rendered config: %s", strings.TrimSpace(lastLines(string(res.Output), 3)))
	}
	return nil
}

func (d *Driver) binaryMissing() bool {
	_, err := os.Stat(d.Paths.XrayBinary())
	return errors.Is(err, os.ErrNotExist)
}

// Health implements driver.Driver.
func (d *Driver) Health(ctx context.Context) model.ComponentHealth {
	h := model.ComponentHealth{Name: model.ComponentXray, Status: model.StatusUnknown, LastError: d.lastErr}
	if !d.lastApply.IsZero() {
		t := d.lastApply
		h.LastApply = &t
	}
	if d.binaryMissing() {
		h.Status = model.StatusDown
		if h.LastError == "" {
			h.LastError = "xray binary not installed"
		}
		return h
	}
	st, err := d.Systemd.State(ctx, d.Unit)
	switch {
	case err != nil:
		h.Status = model.StatusUnknown
		h.LastError = err.Error()
	case !st.Active():
		h.Status = model.StatusDown
	case d.lastErr != "":
		h.Status = model.StatusDegraded
	default:
		h.Status = model.StatusActive
	}
	return h
}

// Version runs "xray version" and returns the first line, or "".
func (d *Driver) Version(ctx context.Context) string {
	if d.binaryMissing() {
		return ""
	}
	res, err := d.Run.Run(ctx, d.Paths.XrayBinary(), "version")
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(res.Output), "\n")
	fields := strings.Fields(line)
	if len(fields) >= 2 && fields[0] == "Xray" {
		return fields[1]
	}
	return strings.TrimSpace(line)
}

func diffOutbounds(prev, next map[string]json.RawMessage) (add, remove []string) {
	for tag, raw := range next {
		if old, ok := prev[tag]; !ok || string(old) != string(raw) {
			add = append(add, tag)
			if ok {
				remove = append(remove, tag)
			}
		}
	}
	for tag := range prev {
		if _, ok := next[tag]; !ok {
			remove = append(remove, tag)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
}

// outboundsFromConfig extracts node outbounds (tag "node-*") from a config
// file so the hot path can diff against what the running instance loaded.
func outboundsFromConfig(cfg []byte) map[string]json.RawMessage {
	var c struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if json.Unmarshal(cfg, &c) != nil {
		return nil
	}
	out := map[string]json.RawMessage{}
	for _, raw := range c.Outbounds {
		var t struct {
			Tag string `json:"tag"`
		}
		if json.Unmarshal(raw, &t) == nil && strings.HasPrefix(t.Tag, "node-") {
			// Re-marshal compactly so byte comparison with the renderer's
			// compact output is meaningful.
			var v any
			if json.Unmarshal(raw, &v) == nil {
				if b, err := json.Marshal(v); err == nil {
					out[t.Tag] = b
				}
			}
		}
	}
	return out
}

func countRules(rules json.RawMessage) int {
	var arr []json.RawMessage
	if json.Unmarshal(rules, &arr) != nil {
		return 0
	}
	return len(arr)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
