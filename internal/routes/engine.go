// Package routes computes the set of IPv4 prefixes the gateway must publish
// over OSPF in Mode B and C, from the desired state, the geodata and the
// resolve cache. It is a pure function of those inputs so the renderer can
// call it and tests can pin its behaviour.
//
// Mode B publishes the Fake-IP pool plus ip/geoip rules. Mode C publishes
// ip/geoip rules, resolved domain rules and geosite rules (via the same-named
// geoip tag when one exists, else by resolving the tag's concrete domains).
// Only rules whose action sends traffic to a proxy are published: a direct
// rule means the main router should not steer that traffic here at all.
package routes

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/geo"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/rulespec"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// IPSource is the geoip lookup the engine needs; nil means not installed.
type IPSource interface {
	HasTag(tag string) (bool, error)
	CIDRs(tag string) ([]netip.Prefix, error)
}

// SiteSource is the geosite lookup; nil means not installed.
type SiteSource interface {
	Entries(tag string) ([]geo.Entry, error)
}

// Sources are the engine's optional inputs.
type Sources struct {
	GeoIP   IPSource
	GeoSite SiteSource
	// DefaultIface is the host's default-route interface, the fallback when
	// the service interface is not configured ("" = no fallback).
	DefaultIface string
}

// State of a candidate route.
const (
	StatePending   = "pending"   // computed, waiting for the kernel
	StatePublished = "published" // installed (set by the driver)
	StateFailed    = "failed"    // rejected by policy
	StateProtected = "protected" // excluded: would loop back to a node/host
)

// Route is one candidate with provenance.
type Route struct {
	Prefix netip.Prefix
	Source string // rule | geo | domain | fakeip
	RuleID string
	State  string
	Detail string
}

// Result is the engine output.
type Result struct {
	// Publish is the final, pruned, sorted set to install.
	Publish []Route
	// Rejected holds candidates that policy refused (failed/protected), for
	// the UI.
	Rejected []Route
	// Domains lists every name the resolver must keep fresh for this state.
	Domains []string
	// Warnings are human-readable findings.
	Warnings []string
	// MaxGeositeDomains caps expansion per tag; exceeding it is a warning.
	Truncated int
}

// MaxGeositeDomains bounds how many concrete domains one geosite tag may
// contribute to Mode C resolution. Beyond this a tag should be expressed as
// a geoip rule instead.
const MaxGeositeDomains = 2000

// Compute builds the route set for snap.
func Compute(snap *desired.Snapshot, src Sources) Result {
	var res Result
	mode := snap.Network.Mode
	if mode == model.ModeA {
		return res
	}
	protected, protectedNames := protectedPrefixes(snap)
	allow := allowlist(snap.Network.OSPFPublishAllow)

	var cands []Route
	add := func(p netip.Prefix, source, ruleID, detail string) {
		cands = append(cands, Route{Prefix: p.Masked(), Source: source, RuleID: ruleID, Detail: detail})
	}

	if mode == model.ModeB {
		if pool, err := netip.ParsePrefix(snap.Network.FakeIPPool); err == nil {
			add(pool, "fakeip", "", "Fake-IP pool")
		} else {
			res.Warnings = append(res.Warnings, "invalid fakeip pool "+snap.Network.FakeIPPool)
		}
	}

	resolved := map[string][]string{}
	for _, e := range snap.Resolved {
		resolved[e.Domain] = e.IPs
	}
	wantDomains := map[string]bool{}

	for _, rule := range snap.ActiveRules() {
		if !proxyAction(rule.ActionKind) {
			continue
		}
		switch rule.MatchKind {
		case model.MatchIP:
			if v, err := rulespec.NormalizeIPOrCIDR(rule.MatchValue); err == nil {
				if p, err := netip.ParsePrefix(v); err == nil && p.Addr().Is4() {
					add(p, "rule", rule.ID, "ip rule")
				}
			}
		case model.MatchGeoIP:
			tag := rule.MatchValue
			if tag == "private" {
				continue // never published: RFC1918 supernets are refused anyway
			}
			if src.GeoIP == nil {
				res.Warnings = append(res.Warnings, "geoip:"+tag+" not published: no geodata installed")
				continue
			}
			negate := strings.HasPrefix(tag, "!")
			if negate {
				res.Warnings = append(res.Warnings, "geoip:"+tag+": negated tags cannot be expressed as routes; skipped")
				continue
			}
			cidrs, err := src.GeoIP.CIDRs(tag)
			if err != nil {
				res.Warnings = append(res.Warnings, "geoip:"+tag+" not published: "+err.Error())
				continue
			}
			for _, c := range cidrs {
				if c.Addr().Is4() {
					add(c, "geo", rule.ID, "geoip:"+tag)
				}
			}
		case model.MatchDomain:
			if mode != model.ModeC {
				continue
			}
			p, err := rulespec.ParseDomain(rule.MatchValue)
			if err != nil || p.Wildcard {
				continue
			}
			wantDomains[p.Base] = true
			for _, ip := range resolved[p.Base] {
				if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
					add(netip.PrefixFrom(a, 32), "domain", rule.ID, p.Base)
				}
			}
		case model.MatchGeosite:
			if mode != model.ModeC {
				continue
			}
			tag := rule.MatchValue
			if src.GeoIP != nil {
				if ok, _ := src.GeoIP.HasTag(tag); ok {
					cidrs, err := src.GeoIP.CIDRs(tag)
					if err == nil {
						for _, c := range cidrs {
							if c.Addr().Is4() {
								add(c, "geo", rule.ID, "geosite:"+tag+" via geoip:"+tag)
							}
						}
						continue
					}
				}
			}
			if src.GeoSite == nil {
				res.Warnings = append(res.Warnings, "geosite:"+tag+" not published: no geodata installed")
				continue
			}
			entries, err := src.GeoSite.Entries(tag)
			if err != nil {
				res.Warnings = append(res.Warnings, "geosite:"+tag+" not published: "+err.Error())
				continue
			}
			n := 0
			for _, e := range entries {
				if !e.Resolvable() {
					continue
				}
				if n >= MaxGeositeDomains {
					res.Truncated++
					continue
				}
				n++
				wantDomains[e.Value] = true
				for _, ip := range resolved[e.Value] {
					if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
						add(netip.PrefixFrom(a, 32), "domain", rule.ID, "geosite:"+tag+" "+e.Value)
					}
				}
			}
			if res.Truncated > 0 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("geosite:%s has more than %d resolvable domains; the rest are not tracked. Use a geoip rule for large sets.", tag, MaxGeositeDomains))
			}
		}
	}

	for d := range wantDomains {
		res.Domains = append(res.Domains, d)
	}
	sort.Strings(res.Domains)

	// Policy pass.
	var ok []Route
	for _, r := range cands {
		if err := validate(r.Prefix); err != nil {
			r.State, r.Detail = StateFailed, r.Detail+": "+err.Error()
			res.Rejected = append(res.Rejected, r)
			continue
		}
		if name := coveredBy(r.Prefix, protected, protectedNames); name != "" {
			r.State, r.Detail = StateProtected, r.Detail+": contains endpoint "+name
			res.Rejected = append(res.Rejected, r)
			continue
		}
		if len(allow) > 0 && !inAllowlist(r.Prefix, allow) {
			r.State, r.Detail = StateFailed, r.Detail+": outside publish allowlist"
			res.Rejected = append(res.Rejected, r)
			continue
		}
		r.State = StatePending
		ok = append(ok, r)
	}
	res.Publish = prune(ok)
	return res
}

func proxyAction(k model.ActionKind) bool {
	return k == model.ActionProxy || k == model.ActionNode || k == model.ActionGroup
}

// validate refuses prefixes that must never enter OSPF: default route,
// loopback, link-local, multicast/reserved, and RFC1918 supernets (a /8 of
// 10.0.0.0 announced to the main router would swallow the LAN).
func validate(p netip.Prefix) error {
	a := p.Addr()
	if !a.Is4() {
		return fmt.Errorf("not IPv4")
	}
	if p.Bits() == 0 {
		return fmt.Errorf("default route")
	}
	if a.IsUnspecified() {
		return fmt.Errorf("unspecified address")
	}
	if a.IsLoopback() {
		return fmt.Errorf("loopback")
	}
	if a.IsLinkLocalUnicast() {
		return fmt.Errorf("link-local")
	}
	b := a.As4()
	if b[0] >= 224 {
		return fmt.Errorf("multicast or reserved")
	}
	// Private, CGNAT and "this network" space is never announced, not even
	// as a /32: a poisoned DNS answer pointing a proxied domain at a LAN
	// host would otherwise pull that host's traffic into the gateway.
	switch {
	case b[0] == 10 || b[0] == 0:
		return fmt.Errorf("private or reserved address space")
	case b[0] == 172 && b[1] >= 16 && b[1] <= 31:
		return fmt.Errorf("private address space")
	case b[0] == 192 && b[1] == 168:
		return fmt.Errorf("private address space")
	case b[0] == 100 && b[1] >= 64 && b[1] <= 127:
		return fmt.Errorf("CGNAT address space")
	}
	return nil
}

// protectedPrefixes collects the /32s of every node and managed host so the
// path to them is never attracted back to the gateway (the OSPF loop).
func protectedPrefixes(snap *desired.Snapshot) ([]netip.Prefix, map[netip.Prefix]string) {
	var out []netip.Prefix
	names := map[netip.Prefix]string{}
	addAddr := func(host, name string) {
		if a, err := netip.ParseAddr(host); err == nil && a.Is4() {
			p := netip.PrefixFrom(a, 32)
			out = append(out, p)
			names[p] = name
			return
		}
		for _, e := range snap.Resolved {
			if e.Domain == strings.ToLower(host) {
				for _, ip := range e.IPs {
					if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
						p := netip.PrefixFrom(a, 32)
						out = append(out, p)
						names[p] = name
					}
				}
			}
		}
	}
	for _, n := range snap.Nodes {
		addAddr(n.Address, "node "+n.Name)
	}
	for _, h := range snap.ProtectedHosts {
		addAddr(h, "host "+h)
	}
	return out, names
}

// ProtectedDomains lists node/host names that must be resolved so they can
// be protected; the resolver keeps them fresh alongside rule domains.
func ProtectedDomains(snap *desired.Snapshot) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			return
		}
		if _, err := netip.ParseAddr(h); err == nil {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, n := range snap.Nodes {
		add(n.Address)
	}
	for _, h := range snap.ProtectedHosts {
		add(h)
	}
	sort.Strings(out)
	return out
}

func coveredBy(p netip.Prefix, protected []netip.Prefix, names map[netip.Prefix]string) string {
	for _, pp := range protected {
		if p.Contains(pp.Addr()) {
			return names[pp] + " " + pp.Addr().String()
		}
	}
	return ""
}

func allowlist(list []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range list {
		if p, err := netip.ParsePrefix(s); err == nil && p.Addr().Is4() {
			out = append(out, p.Masked())
		}
	}
	return out
}

func inAllowlist(p netip.Prefix, allow []netip.Prefix) bool {
	for _, a := range allow {
		if a.Bits() <= p.Bits() && a.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// prune drops prefixes already covered by a broader one, de-duplicates and
// sorts. Provenance of the survivor is the broader route's.
func prune(in []Route) []Route {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Prefix.Bits() != in[j].Prefix.Bits() {
			return in[i].Prefix.Bits() < in[j].Prefix.Bits()
		}
		return in[i].Prefix.Addr().Less(in[j].Prefix.Addr())
	})
	var out []Route
	for _, r := range in {
		covered := false
		for _, o := range out {
			if o.Prefix.Bits() <= r.Prefix.Bits() && o.Prefix.Contains(r.Prefix.Addr()) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Prefix.Addr() != out[j].Prefix.Addr() {
			return out[i].Prefix.Addr().Less(out[j].Prefix.Addr())
		}
		return out[i].Prefix.Bits() < out[j].Prefix.Bits()
	})
	return out
}

// Preflight reports what blocks switching snap into mode: protected
// endpoints that candidate routes would attract, and missing prerequisites.
type PreflightResult struct {
	OK        bool     `json:"ok"`
	Mode      string   `json:"mode"`
	Routes    int      `json:"routes"`
	Rejected  int      `json:"rejected"`
	Protected []string `json:"protected_conflicts"`
	Problems  []string `json:"problems"`
	Warnings  []string `json:"warnings"`
}

// Preflight evaluates a mode switch without changing anything.
func Preflight(snap *desired.Snapshot, src Sources, mode model.Mode) PreflightResult {
	pr := PreflightResult{OK: true, Mode: string(mode)}
	if mode == model.ModeA {
		return pr
	}
	s := *snap
	s.Network.Mode = mode
	res := Compute(&s, src)
	pr.Routes, pr.Rejected, pr.Warnings = len(res.Publish), len(res.Rejected), res.Warnings
	for _, r := range res.Rejected {
		if r.State == StateProtected {
			pr.Protected = append(pr.Protected, r.Prefix.String()+" ("+r.Detail+")")
		}
	}
	// Protected candidates are excluded automatically, so they are a
	// warning, not a blocker: the operator should know that those prefixes
	// are not steered here.
	if len(pr.Protected) > 0 {
		pr.Warnings = append(pr.Warnings, fmt.Sprintf("%d route(s) contain a node or managed host address and are excluded to avoid a loop", len(pr.Protected)))
	}
	if len(snap.EnabledNodes()) == 0 {
		pr.Problems = append(pr.Problems, "no enabled node: in Mode B/C every steered flow needs a proxy outbound")
	}
	if snap.Network.ServiceIface == "" && src.DefaultIface == "" {
		pr.Problems = append(pr.Problems, "service interface not set: OSPF must know which LAN network to join (network <subnet> area 0)")
	} else if snap.Network.ServiceIface == "" {
		pr.Warnings = append(pr.Warnings, "service interface not set; "+src.DefaultIface+" (default route) will be used")
	}
	for _, rule := range snap.Rules {
		if rule.MatchKind == model.MatchDomain {
			if p, err := rulespec.ParseDomain(rule.MatchValue); err == nil && p.Wildcard {
				pr.Problems = append(pr.Problems, "wildcard domain rule "+rule.MatchValue+" cannot be expressed as routes; remove it or use Mode A")
			}
		}
	}
	if mode == model.ModeB {
		if _, err := netip.ParsePrefix(snap.Network.FakeIPPool); err != nil {
			pr.Problems = append(pr.Problems, "invalid Fake-IP pool "+snap.Network.FakeIPPool)
		}
	}
	pr.OK = len(pr.Problems) == 0
	return pr
}

// ToPublished converts a Result into store rows.
func ToPublished(res Result) []store.PublishedRoute {
	out := make([]store.PublishedRoute, 0, len(res.Publish)+len(res.Rejected))
	for _, r := range res.Publish {
		out = append(out, store.PublishedRoute{CIDR: r.Prefix.String(), Source: r.Source, RuleID: r.RuleID, State: r.State, Detail: r.Detail})
	}
	for _, r := range res.Rejected {
		out = append(out, store.PublishedRoute{CIDR: r.Prefix.String(), Source: r.Source, RuleID: r.RuleID, State: r.State, Detail: r.Detail})
	}
	return out
}
