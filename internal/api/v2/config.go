package v2

import (
	"database/sql"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/routes"
	"github.com/zlylong/EdgeRouteGW/internal/rulespec"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func (d Deps) getDNS(w http.ResponseWriter, r *http.Request) {
	c, err := d.Store.DNSConfig(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemResp[model.DNSConfig]{Item: c})
}

// DNSInput is the full DNS configuration (PUT replaces all fields).
type DNSInput struct {
	LocalUpstreams  []string `json:"local_upstreams"`
	RemoteUpstreams []string `json:"remote_upstreams"`
	CacheSize       int      `json:"cache_size"`
	LazyCache       bool     `json:"lazy_cache"`
	LazyCacheTTL    int      `json:"lazy_cache_ttl"`
	LogLevel        string   `json:"log_level"`
	Hijack          *bool    `json:"hijack"`    // nil = keep default (on)
	BlockDoT        *bool    `json:"block_dot"` // nil = keep default (on)
	Tap             *bool    `json:"tap"`       // nil = keep default (on)
}

func (in *DNSInput) validate() (model.DNSConfig, error) {
	c := model.DNSConfig{CacheSize: in.CacheSize, LazyCache: in.LazyCache, LazyCacheTTL: in.LazyCacheTTL, LogLevel: strings.ToLower(strings.TrimSpace(in.LogLevel)), Hijack: true, BlockDoT: true, Tap: true}
	if in.Tap != nil {
		c.Tap = *in.Tap
	}
	if in.Hijack != nil {
		c.Hijack = *in.Hijack
	}
	if in.BlockDoT != nil {
		c.BlockDoT = *in.BlockDoT
	}
	norm := func(field string, list []string) ([]string, error) {
		if len(list) == 0 {
			return nil, invalid(field, "at least one upstream")
		}
		if len(list) > 8 {
			return nil, invalid(field, "at most 8 upstreams")
		}
		out := make([]string, 0, len(list))
		for _, u := range list {
			v, err := rulespec.NormalizeUpstream(u)
			if err != nil {
				return nil, invalid(field, "%v", err)
			}
			out = append(out, v)
		}
		return out, nil
	}
	var err error
	if c.LocalUpstreams, err = norm("local_upstreams", in.LocalUpstreams); err != nil {
		return c, err
	}
	if c.RemoteUpstreams, err = norm("remote_upstreams", in.RemoteUpstreams); err != nil {
		return c, err
	}
	if c.CacheSize < 0 || c.CacheSize > 1_000_000 {
		return c, invalid("cache_size", "must be 0-1000000")
	}
	if c.LazyCacheTTL < 0 || c.LazyCacheTTL > 7*86400 {
		return c, invalid("lazy_cache_ttl", "must be 0-604800 seconds")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	case "":
		c.LogLevel = "info"
	default:
		return c, invalid("log_level", "must be debug, info, warn or error")
	}
	return c, nil
}

func (d Deps) putDNS(w http.ResponseWriter, r *http.Request) {
	var in DNSInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	c, err := in.validate()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	ctx := r.Context()
	cur, err := d.Store.DNSConfig(ctx)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	gen, err := d.mutate(ctx, r, "update DNS settings", func(tx *sql.Tx) error {
		return store.UpdateDNSConfig(ctx, tx, c)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	// Answers depend on the upstreams; a different resolver path means the
	// cached routes may no longer match what clients get.
	if strings.Join(cur.LocalUpstreams, ",") != strings.Join(c.LocalUpstreams, ",") || strings.Join(cur.RemoteUpstreams, ",") != strings.Join(c.RemoteUpstreams, ",") {
		_ = d.Store.ClearResolveCache(ctx)
		if d.ResolverKick != nil {
			d.ResolverKick()
		}
	}
	saved, _ := d.Store.DNSConfig(ctx)
	writeJSON(w, http.StatusOK, itemResp[model.DNSConfig]{Item: saved, Generation: gen})
}

func (d Deps) getNetwork(w http.ResponseWriter, r *http.Request) {
	c, err := d.Store.NetworkConfig(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemResp[model.NetworkConfig]{Item: c})
}

// NetworkInput is the full network configuration (PUT replaces all fields).
// Mode switches to B/C will gain a preflight (protected endpoint vs route
// conflicts) in M3; today they are validated for shape only.
type NetworkInput struct {
	Mode                string   `json:"mode"`
	ManagementIface     string   `json:"management_iface"`
	ServiceIface        string   `json:"service_iface"`
	FakeIPPool          string   `json:"fakeip_pool"`
	TProxyPort          int      `json:"tproxy_port"`
	LANDefaultPolicy    string   `json:"lan_default_policy"`
	OSPFPushBatch       int      `json:"ospf_push_batch"`
	OSPFPushIntervalSec int      `json:"ospf_push_interval_s"`
	OSPFResolveWorkers  int      `json:"ospf_resolve_workers"`
	OSPFPublishAllow    []string `json:"ospf_publish_allowlist"`
	NodeFailoverMode    string   `json:"node_failover_mode"`
	SniffOverride       bool     `json:"sniff_override"`
}

func (in *NetworkInput) validate() (model.NetworkConfig, error) {
	c := model.NetworkConfig{
		Mode:                model.Mode(strings.ToUpper(strings.TrimSpace(in.Mode))),
		ManagementIface:     strings.TrimSpace(in.ManagementIface),
		ServiceIface:        strings.TrimSpace(in.ServiceIface),
		FakeIPPool:          strings.TrimSpace(in.FakeIPPool),
		TProxyPort:          in.TProxyPort,
		LANDefaultPolicy:    strings.ToLower(strings.TrimSpace(in.LANDefaultPolicy)),
		OSPFPushBatch:       in.OSPFPushBatch,
		OSPFPushIntervalSec: in.OSPFPushIntervalSec,
		OSPFResolveWorkers:  in.OSPFResolveWorkers,
		NodeFailoverMode:    strings.ToLower(strings.TrimSpace(in.NodeFailoverMode)),
		SniffOverride:       in.SniffOverride,
	}
	if !c.Mode.Valid() {
		return c, invalid("mode", "must be A, B or C")
	}
	for _, f := range []struct{ name, v string }{{"management_iface", c.ManagementIface}, {"service_iface", c.ServiceIface}} {
		if f.v != "" && (!ifaceNameRe.MatchString(f.v) || strings.Contains(f.v, "..")) {
			return c, invalid(f.name, "invalid interface name")
		}
	}
	if c.FakeIPPool == "" {
		c.FakeIPPool = "198.18.0.0/16"
	}
	pool, err := netip.ParsePrefix(c.FakeIPPool)
	if err != nil || !pool.Addr().Is4() || pool.Bits() > 24 {
		return c, invalid("fakeip_pool", "must be an IPv4 prefix of /24 or larger (default 198.18.0.0/16)")
	}
	c.FakeIPPool = pool.Masked().String()
	if c.TProxyPort == 0 {
		c.TProxyPort = 12345
	}
	if c.TProxyPort < 1024 || c.TProxyPort > 65535 {
		return c, invalid("tproxy_port", "must be 1024-65535")
	}
	switch c.LANDefaultPolicy {
	case "proxy", "direct":
	case "":
		c.LANDefaultPolicy = "proxy"
	default:
		return c, invalid("lan_default_policy", "must be proxy or direct")
	}
	switch c.NodeFailoverMode {
	case "normal", "strict":
	case "":
		c.NodeFailoverMode = "normal"
	default:
		return c, invalid("node_failover_mode", "must be normal or strict")
	}
	c.OSPFPushBatch = clamp(c.OSPFPushBatch, 50, 5000, 500)
	c.OSPFPushIntervalSec = clamp(c.OSPFPushIntervalSec, 1, 60, 2)
	c.OSPFResolveWorkers = clamp(c.OSPFResolveWorkers, 1, 64, 8)
	c.OSPFPublishAllow = []string{}
	for _, a := range in.OSPFPublishAllow {
		v, err := rulespec.NormalizeIPOrCIDR(a)
		if err != nil {
			return c, invalid("ospf_publish_allowlist", "%v", err)
		}
		c.OSPFPublishAllow = append(c.OSPFPublishAllow, v)
	}
	return c, nil
}

func clamp(v, lo, hi, def int) int {
	if v == 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (d Deps) putNetwork(w http.ResponseWriter, r *http.Request) {
	var in NetworkInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	c, err := in.validate()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	ctx := r.Context()
	cur, err := d.Store.NetworkConfig(ctx)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if c.Mode != model.ModeA && c.Mode != cur.Mode {
		// A switch into B/C runs the same preflight the UI shows; the
		// candidate routes are computed with the new settings.
		snap, err := desired.Load(ctx, d.Store, nil)
		if err != nil {
			d.fail(w, r, err)
			return
		}
		snap.Network = c
		pr := routes.Preflight(snap, d.sources(), c.Mode)
		if !pr.OK {
			writeErr(w, http.StatusUnprocessableEntity, codeValidation, "mode switch blocked: "+strings.Join(pr.Problems, "; "), map[string]any{"field": "mode", "preflight": pr})
			return
		}
	}
	summary := "update network settings"
	if cur.Mode != c.Mode {
		summary = "switch mode " + string(cur.Mode) + " -> " + string(c.Mode)
	}
	gen, err := d.mutate(ctx, r, summary, func(tx *sql.Tx) error {
		return store.UpdateNetworkConfig(ctx, tx, c)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	saved, _ := d.Store.NetworkConfig(ctx)
	writeJSON(w, http.StatusOK, itemResp[model.NetworkConfig]{Item: saved, Generation: gen})
}

// ifaceNameRe is what the kernel accepts for an interface name; it also
// keeps the value safe inside FRR configuration and sysfs paths.
var ifaceNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
