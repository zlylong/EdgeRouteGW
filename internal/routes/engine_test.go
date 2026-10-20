package routes

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/geo"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

type fakeIP map[string][]string

func (f fakeIP) HasTag(tag string) (bool, error) { _, ok := f[tag]; return ok, nil }
func (f fakeIP) CIDRs(tag string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range f[tag] {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out, nil
}

type fakeSite map[string][]geo.Entry

func (f fakeSite) Entries(tag string) ([]geo.Entry, error) { return f[tag], nil }

func snap(mode model.Mode) *desired.Snapshot {
	return &desired.Snapshot{
		Network:  model.NetworkConfig{Mode: mode, FakeIPPool: "198.18.0.0/16", ServiceIface: "eth0", OSPFPublishAllow: []string{}},
		Nodes:    []model.Node{{ID: "n1", Name: "SG", Address: "203.0.113.10", Port: 443, Enabled: true}, {ID: "n2", Name: "HK", Address: "hk.example.net", Port: 443, Enabled: true}},
		RuleSets: []model.RuleSet{{ID: "s", Enabled: true}},
		Rules: []model.Rule{
			{ID: "r-ip", SetID: "s", MatchKind: model.MatchIP, MatchValue: "8.8.8.0/24", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r-ip-direct", SetID: "s", MatchKind: model.MatchIP, MatchValue: "9.9.9.9/32", ActionKind: model.ActionDirect, Enabled: true},
			{ID: "r-geoip", SetID: "s", MatchKind: model.MatchGeoIP, MatchValue: "telegram", ActionKind: model.ActionNode, ActionRef: "n1", Enabled: true},
			{ID: "r-dom", SetID: "s", MatchKind: model.MatchDomain, MatchValue: "youtube.com", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r-site", SetID: "s", MatchKind: model.MatchGeosite, MatchValue: "netflix", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r-site-ip", SetID: "s", MatchKind: model.MatchGeosite, MatchValue: "cloudflare", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r-bad", SetID: "s", MatchKind: model.MatchIP, MatchValue: "10.0.0.0/8", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r-node", SetID: "s", MatchKind: model.MatchIP, MatchValue: "203.0.113.0/24", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r-cover", SetID: "s", MatchKind: model.MatchIP, MatchValue: "8.8.8.8/32", ActionKind: model.ActionProxy, Enabled: true},
		},
		Resolved: []store.ResolvedEntry{
			{Domain: "youtube.com", Group: "local", IPs: []string{"142.250.1.1", "142.250.1.2"}},
			{Domain: "netflix.com", Group: "local", IPs: []string{"54.1.1.1"}},
			{Domain: "hk.example.net", Group: "local", IPs: []string{"198.51.100.7"}},
		},
	}
}

func sources() Sources {
	return Sources{
		GeoIP:   fakeIP{"telegram": {"91.108.4.0/22", "149.154.160.0/20"}, "cloudflare": {"104.16.0.0/13"}},
		GeoSite: fakeSite{"netflix": {{Kind: geo.KindRoot, Value: "netflix.com"}, {Kind: geo.KindPlain, Value: "nflx"}}},
	}
}

func prefixes(rs []Route) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Prefix.String())
	}
	return out
}

func TestModeAPublishesNothing(t *testing.T) {
	res := Compute(snap(model.ModeA), sources())
	if len(res.Publish) != 0 || len(res.Domains) != 0 {
		t.Fatalf("mode A: %+v", res)
	}
}

func TestModeBPublishesPoolAndIPRules(t *testing.T) {
	res := Compute(snap(model.ModeB), sources())
	got := strings.Join(prefixes(res.Publish), " ")
	want := "8.8.8.0/24 91.108.4.0/22 149.154.160.0/20 198.18.0.0/16"
	if got != want {
		t.Fatalf("publish %q want %q", got, want)
	}
	if len(res.Domains) != 0 {
		t.Fatalf("mode B must not need resolution, got %v", res.Domains)
	}
	// 8.8.8.8/32 pruned by /24; 10.0.0.0/8 failed; 203.0.113.0/24 protected (node n1).
	states := map[string]string{}
	for _, r := range res.Rejected {
		states[r.Prefix.String()] = r.State
	}
	if states["10.0.0.0/8"] != StateFailed || states["203.0.113.0/24"] != StateProtected {
		t.Fatalf("rejected %v", states)
	}
}

func TestModeCResolvesDomainsAndGeosite(t *testing.T) {
	res := Compute(snap(model.ModeC), sources())
	got := strings.Join(prefixes(res.Publish), " ")
	// youtube resolved /32s, netflix via geosite expansion, cloudflare via same-named geoip.
	for _, want := range []string{"142.250.1.1/32", "142.250.1.2/32", "54.1.1.1/32", "104.16.0.0/13", "8.8.8.0/24"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %q", want, got)
		}
	}
	if strings.Contains(got, "198.18.0.0/16") {
		t.Fatal("mode C must not publish the fake-ip pool")
	}
	if strings.Join(res.Domains, ",") != "netflix.com,youtube.com" {
		t.Fatalf("domains %v", res.Domains)
	}
	// hk.example.net resolved to 198.51.100.7, which must be protected if a rule covers it.
	s := snap(model.ModeC)
	s.Rules = append(s.Rules, model.Rule{ID: "r-hk", SetID: "s", MatchKind: model.MatchIP, MatchValue: "198.51.100.0/24", ActionKind: model.ActionProxy, Enabled: true})
	res = Compute(s, sources())
	found := false
	for _, r := range res.Rejected {
		if r.Prefix.String() == "198.51.100.0/24" && r.State == StateProtected && strings.Contains(r.Detail, "node HK") {
			found = true
		}
	}
	if !found {
		t.Fatalf("resolved node address not protected: %+v", res.Rejected)
	}
	if pd := ProtectedDomains(s); len(pd) != 1 || pd[0] != "hk.example.net" {
		t.Fatalf("protected domains %v", pd)
	}
}

func TestAllowlistAndPreflight(t *testing.T) {
	s := snap(model.ModeB)
	s.Network.OSPFPublishAllow = []string{"8.0.0.0/8"}
	res := Compute(s, sources())
	if got := strings.Join(prefixes(res.Publish), " "); got != "8.8.8.0/24" {
		t.Fatalf("allowlist publish %q", got)
	}
	pr := Preflight(snap(model.ModeA), sources(), model.ModeC)
	if !pr.OK || pr.Routes == 0 || len(pr.Protected) == 0 {
		t.Fatalf("preflight %+v", pr)
	}
	s2 := snap(model.ModeA)
	s2.Network.ServiceIface = ""
	s2.Nodes = nil
	pr = Preflight(s2, sources(), model.ModeB)
	if pr.OK || len(pr.Problems) < 2 {
		t.Fatalf("preflight should fail without iface and nodes: %+v", pr)
	}
}
