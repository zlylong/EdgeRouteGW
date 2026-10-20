// Package trace answers "where would traffic to X go?" by walking the active
// rules the same way the renderer emits them. It mirrors Xray's matching
// (full/suffix/single-level domains, geosite entry kinds, CIDR and geoip)
// closely enough for the routing page; the authoritative answer remains
// Xray's own RoutingService.TestRoute, which the gRPC read path will expose
// later.
package trace

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/geo"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/rulespec"
)

// Sources are the optional geodata readers.
type Sources struct {
	Site *geo.Site
	IP   *geo.IP
}

// Result is the trace outcome.
type Result struct {
	Target     string      `json:"target"`
	Kind       string      `json:"kind"` // domain or ip
	Matched    *model.Rule `json:"matched_rule,omitempty"`
	RuleSet    string      `json:"rule_set,omitempty"`
	Action     string      `json:"action"`        // direct|block|proxy|node|group|default
	Outbound   string      `json:"outbound"`      // outbound tag or balancer tag
	Via        string      `json:"via,omitempty"` // node/group name
	Reason     string      `json:"reason"`
	Considered int         `json:"considered"` // rules evaluated
	Notes      []string    `json:"notes,omitempty"`
}

// Run evaluates target against snap.
func Run(snap *desired.Snapshot, src Sources, target string) Result {
	target = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(target, ".")))
	res := Result{Target: target, Kind: "domain"}
	var ip netip.Addr
	if a, err := netip.ParseAddr(target); err == nil {
		ip, res.Kind = a.Unmap(), "ip"
	}
	setNames := map[string]string{}
	for _, s := range snap.RuleSets {
		setNames[s.ID] = s.Name
	}
	for _, rule := range snap.ActiveRules() {
		res.Considered++
		hit, note := matches(rule, target, ip, src)
		if note != "" {
			res.Notes = append(res.Notes, note)
		}
		if !hit {
			continue
		}
		r := rule
		res.Matched = &r
		res.RuleSet = setNames[rule.SetID]
		res.Action = string(rule.ActionKind)
		res.Outbound, res.Via, res.Reason = resolve(snap, rule)
		return res
	}
	res.Action = "default"
	switch {
	case snap.Network.Mode == model.ModeA:
		res.Outbound, res.Reason = render.TagDirect, "no rule matched; Mode A sends unmatched traffic direct"
	case snap.Network.LANDefaultPolicy == "proxy":
		res.Outbound, res.Via = defaultOutbound(snap)
		res.Reason = "no rule matched; LAN default policy is proxy"
	default:
		res.Outbound, res.Reason = render.TagDirect, "no rule matched; LAN default policy is direct"
	}
	if res.Kind == "domain" && snap.Network.Mode != model.ModeA {
		res.Notes = append(res.Notes, "Mode B/C: whether the main router steers this traffic here depends on the published routes, not only on these rules")
	}
	return res
}

func defaultOutbound(snap *desired.Snapshot) (string, string) {
	if n := snap.DefaultNode(); n != nil {
		return render.NodeTag(n.ID), n.Name
	}
	if len(snap.EnabledNodes()) > 0 {
		return render.TagAutoProxy, "leastPing over all enabled nodes"
	}
	return render.TagDirect, ""
}

func resolve(snap *desired.Snapshot, rule model.Rule) (outbound, via, reason string) {
	switch rule.ActionKind {
	case model.ActionDirect:
		return render.TagDirect, "", "rule action direct"
	case model.ActionBlock:
		return render.TagBlock, "", "rule action block"
	case model.ActionNode:
		n := snap.Node(rule.ActionRef)
		if n != nil && n.Enabled {
			return render.NodeTag(n.ID), n.Name, "rule action node"
		}
		return fallback(snap, "node is missing or disabled")
	case model.ActionGroup:
		g := snap.Group(rule.ActionRef)
		if g == nil {
			return fallback(snap, "group is missing")
		}
		var live []*model.Node
		for _, id := range g.MemberIDs {
			if n := snap.Node(id); n != nil && n.Enabled {
				live = append(live, n)
			}
		}
		switch len(live) {
		case 0:
			return fallback(snap, fmt.Sprintf("group %q has no enabled member", g.Name))
		case 1:
			return render.NodeTag(live[0].ID), live[0].Name, fmt.Sprintf("group %q has one enabled member", g.Name)
		}
		return render.GroupTag(g.ID), g.Name, fmt.Sprintf("rule action group (%s)", g.Kind)
	default:
		o, v := defaultOutbound(snap)
		return o, v, "rule action proxy (default outbound)"
	}
}

func fallback(snap *desired.Snapshot, why string) (string, string, string) {
	if snap.Network.NodeFailoverMode == "strict" {
		return render.TagBlock, "", why + "; strict failover blocks"
	}
	o, v := defaultOutbound(snap)
	return o, v, why + "; normal failover uses default outbound"
}

func matches(rule model.Rule, target string, ip netip.Addr, src Sources) (bool, string) {
	isIP := ip.IsValid()
	switch rule.MatchKind {
	case model.MatchDomain:
		if isIP {
			return false, ""
		}
		p, err := rulespec.ParseDomain(rule.MatchValue)
		return err == nil && p.Matches(target), ""
	case model.MatchGeosite:
		if isIP {
			return false, ""
		}
		if src.Site == nil {
			return false, "geosite:" + rule.MatchValue + " not evaluated (no geodata installed)"
		}
		entries, err := src.Site.Entries(rule.MatchValue)
		if err != nil {
			return false, "geosite:" + rule.MatchValue + " not evaluated: " + err.Error()
		}
		for _, e := range entries {
			if entryMatches(e, target) {
				return true, ""
			}
		}
		return false, ""
	case model.MatchIP:
		if !isIP {
			return false, ""
		}
		pfx, err := netip.ParsePrefix(rule.MatchValue)
		return err == nil && pfx.Contains(ip), ""
	case model.MatchGeoIP:
		if !isIP {
			return false, ""
		}
		tag := rule.MatchValue
		if tag == "private" {
			return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast(), ""
		}
		if src.IP == nil {
			return false, "geoip:" + tag + " not evaluated (no geodata installed)"
		}
		negate := strings.HasPrefix(tag, "!")
		cidrs, err := src.IP.CIDRs(strings.TrimPrefix(tag, "!"))
		if err != nil {
			return false, "geoip:" + tag + " not evaluated: " + err.Error()
		}
		in := false
		for _, c := range cidrs {
			if c.Contains(ip) {
				in = true
				break
			}
		}
		return in != negate, ""
	}
	return false, ""
}

func entryMatches(e geo.Entry, host string) bool {
	switch e.Kind {
	case geo.KindFull:
		return host == e.Value
	case geo.KindRoot:
		return host == e.Value || strings.HasSuffix(host, "."+e.Value)
	case geo.KindPlain:
		return strings.Contains(host, e.Value)
	case geo.KindRegex:
		re, err := regexp.Compile(e.Value)
		return err == nil && re.MatchString(host)
	}
	return false
}
