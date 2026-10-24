package render

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/geo"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
)

type fakeGeo map[string][]geo.Entry

func (f fakeGeo) Entries(tag string) ([]geo.Entry, error) {
	e, ok := f[tag]
	if !ok {
		return nil, geo.ErrTagNotFound
	}
	return e, nil
}

func baseSnap() *desired.Snapshot {
	return &desired.Snapshot{
		Generation: 7,
		Network: model.NetworkConfig{Mode: model.ModeA, TProxyPort: 12345, FakeIPPool: "198.18.0.0/16",
			LANDefaultPolicy: "proxy", NodeFailoverMode: "normal"},
		DNS: model.DNSConfig{LocalUpstreams: []string{"119.29.29.29", "223.5.5.5"}, RemoteUpstreams: []string{"1.1.1.1", "https://dns.google/dns-query"},
			CacheSize: 10240, LazyCache: true, LazyCacheTTL: 86400, LogLevel: "info"},
		Nodes: []model.Node{
			{ID: "n1", Name: "SG", Protocol: model.ProtocolVLESS, Address: "sg.example.com", Port: 443, Enabled: true, IsDefault: true, Credential: "uuid-1",
				Params: `{"type":"tcp","security":"reality","sni":"www.apple.com","pbk":"PBK","sid":"ab","fp":"chrome","flow":"xtls-rprx-vision"}`},
			{ID: "n2", Name: "HK", Protocol: model.ProtocolShadowsocks, Address: "1.2.3.4", Port: 8388, Enabled: true, Credential: "pw", Params: `{"method":"chacha20-ietf-poly1305"}`},
			{ID: "n3", Name: "off", Protocol: model.ProtocolTrojan, Address: "x", Port: 1, Enabled: false, Credential: "t"},
		},
		Groups:   []model.OutboundGroup{{ID: "g1", Name: "ha", Kind: model.GroupFailover, MemberIDs: []string{"n1", "n2", "n3"}}},
		RuleSets: []model.RuleSet{{ID: "s1", Name: "main", Position: 0, Enabled: true}, {ID: "s2", Name: "off", Position: 1, Enabled: false}},
		Rules: []model.Rule{
			{ID: "r1", SetID: "s1", Position: 0, MatchKind: model.MatchDomain, MatchValue: "**.youtube.com", ActionKind: model.ActionGroup, ActionRef: "g1", Enabled: true},
			{ID: "r2", SetID: "s1", Position: 1, MatchKind: model.MatchGeosite, MatchValue: "cn", ActionKind: model.ActionDirect, Enabled: true},
			{ID: "r3", SetID: "s1", Position: 2, MatchKind: model.MatchGeoIP, MatchValue: "private", ActionKind: model.ActionDirect, Enabled: true},
			{ID: "r4", SetID: "s1", Position: 3, MatchKind: model.MatchDomain, MatchValue: "*.example.org", ActionKind: model.ActionNode, ActionRef: "n2", Enabled: true},
			{ID: "r5", SetID: "s1", Position: 4, MatchKind: model.MatchIP, MatchValue: "8.8.8.8", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r6", SetID: "s1", Position: 5, MatchKind: model.MatchDomain, MatchValue: "bad..value", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r7", SetID: "s1", Position: 6, MatchKind: model.MatchGeosite, MatchValue: "netflix", ActionKind: model.ActionProxy, Enabled: true},
			{ID: "r8", SetID: "s1", Position: 7, MatchKind: model.MatchDomain, MatchValue: "disabled.com", ActionKind: model.ActionProxy, Enabled: false},
			{ID: "r9", SetID: "s2", Position: 0, MatchKind: model.MatchDomain, MatchValue: "inoffset.com", ActionKind: model.ActionProxy, Enabled: true},
		},
		DevicePolicies: []model.DevicePolicy{
			{ID: "d1", Kind: model.DeviceMAC, Value: "AA:BB:CC:DD:EE:FF", Action: model.DeviceDirect},
			{ID: "d2", Kind: model.DeviceIP, Value: "192.168.1.10", Action: model.DeviceProxy},
			{ID: "d3", Kind: model.DeviceCIDR, Value: "fd00::/64", Action: model.DeviceReject},
		},
		Bypass: []model.BypassDestination{{ID: "b1", CIDR: "203.0.113.5"}},
	}
}

func deps() Deps {
	return Deps{Paths: paths.New("/var/lib/edgeroutegw"), GeoSite: fakeGeo{
		"cn":      {{Kind: geo.KindRoot, Value: "baidu.com"}, {Kind: geo.KindFull, Value: "www.qq.com"}},
		"netflix": {{Kind: geo.KindRoot, Value: "netflix.com"}, {Kind: geo.KindPlain, Value: "nflx"}},
	}}
}

func TestRenderIsDeterministic(t *testing.T) {
	a, err := Render(baseSnap(), deps())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		b, err := Render(baseSnap(), deps())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.Xray, b.Xray) || !bytes.Equal(a.MosdnsConfig, b.MosdnsConfig) || !bytes.Equal(a.MosdnsProxyDomains, b.MosdnsProxyDomains) || !bytes.Equal(a.Nft, b.Nft) || a.XrayStructuralHash != b.XrayStructuralHash {
			t.Fatalf("render %d differs from first render", i)
		}
	}
}

func TestXrayContent(t *testing.T) {
	a, err := Render(baseSnap(), deps())
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
		Routing   struct {
			Rules     []map[string]any `json:"rules"`
			Balancers []map[string]any `json:"balancers"`
		} `json:"routing"`
		Observatory map[string]any `json:"observatory"`
		FakeDNS     any            `json:"fakedns"`
	}
	if err := json.Unmarshal(a.Xray, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.FakeDNS != nil {
		t.Fatal("Mode A must not enable fakedns")
	}
	// direct, block, n1, n2 (n3 disabled)
	if len(cfg.Outbounds) != 4 {
		t.Fatalf("outbounds %d", len(cfg.Outbounds))
	}
	if _, ok := a.XrayOutbounds["node-n1"]; !ok || len(a.XrayOutbounds) != 2 {
		t.Fatalf("XrayOutbounds %v", a.XrayOutbounds)
	}
	n1 := cfg.Outbounds[2]
	if n1["tag"] != "node-n1" || n1["protocol"] != "vless" {
		t.Fatalf("n1 %v", n1)
	}
	for _, k := range shorthandKeys {
		if _, leaked := n1[k]; leaked {
			t.Fatalf("shorthand key %q leaked into outbound", k)
		}
	}
	ss := n1["streamSettings"].(map[string]any)
	rs := ss["realitySettings"].(map[string]any)
	if ss["security"] != "reality" || rs["serverName"] != "www.apple.com" || rs["fingerprint"] != "chrome" || ss["sockopt"].(map[string]any)["mark"].(float64) != 2 {
		t.Fatalf("n1 streamSettings %v", ss)
	}
	user := n1["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["id"] != "uuid-1" || user["flow"] != "xtls-rprx-vision" {
		t.Fatalf("n1 user %v", user)
	}
	n2srv := cfg.Outbounds[3]["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if n2srv["method"] != "chacha20-ietf-poly1305" || n2srv["password"] != "pw" || n2srv["address"] != "1.2.3.4" {
		t.Fatalf("n2 server %v", n2srv)
	}

	// Rules: sys-api, r1..r5, r7 (r6 invalid, r8 disabled, r9 in disabled set), default.
	tags := []string{}
	for _, r := range cfg.Routing.Rules {
		tags = append(tags, r["ruleTag"].(string))
	}
	want := []string{"sys-api", "probe-n1", "probe-n2", "probe-none", "sys-dns-upstream-0", "sys-dns-upstream-1", "rule-r1", "rule-r2", "rule-r3", "rule-r4", "rule-r5", "rule-r7", "default"}
	if strings.Join(tags, ",") != strings.Join(want, ",") {
		t.Fatalf("rule order %v, want %v", tags, want)
	}
	byTag := map[string]map[string]any{}
	for _, r := range cfg.Routing.Rules {
		byTag[r["ruleTag"].(string)] = r
	}
	r1 := byTag["rule-r1"]
	if r1["balancerTag"] != "group-g1" || r1["domain"].([]any)[0] != "domain:youtube.com" {
		t.Fatalf("r1 %v", r1)
	}
	r4 := byTag["rule-r4"]
	if r4["outboundTag"] != "node-n2" || !strings.HasPrefix(r4["domain"].([]any)[0].(string), "regexp:") {
		t.Fatalf("r4 %v", r4)
	}
	r5 := byTag["rule-r5"]
	if r5["outboundTag"] != "node-n1" || r5["ip"].([]any)[0] != "8.8.8.8/32" {
		t.Fatalf("r5 should go to default node: %v", r5)
	}
	def := cfg.Routing.Rules[len(cfg.Routing.Rules)-1]
	if def["outboundTag"] != "node-n1" { // baseSnap: LAN default policy proxy -> default node, in every mode
		t.Fatalf("catch-all must follow the LAN default policy: %v", def)
	}
	if len(cfg.Routing.Balancers) != 1 || cfg.Routing.Balancers[0]["tag"] != "group-g1" || cfg.Routing.Balancers[0]["fallbackTag"] != "node-n2" {
		t.Fatalf("balancers %v", cfg.Routing.Balancers)
	}
	if cfg.Observatory == nil {
		t.Fatal("observatory required when balancers exist")
	}
	if len(a.Warnings) == 0 || !strings.Contains(strings.Join(a.Warnings, "\n"), "rule r6 skipped") {
		t.Fatalf("expected warning for invalid rule, got %v", a.Warnings)
	}
}

func TestModeBAndCatchAll(t *testing.T) {
	s := baseSnap()
	s.Network.Mode = model.ModeB
	a, err := Render(s, deps())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(a.Xray, &cfg)
	if cfg["fakedns"] == nil || cfg["dns"] == nil {
		t.Fatal("Mode B must enable fakedns")
	}
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	if rules[1].(map[string]any)["ruleTag"] != "sys-dns" {
		t.Fatalf("Mode B needs dns-in rule second: %v", rules[1])
	}
	last := rules[len(rules)-1].(map[string]any)
	if last["outboundTag"] != "node-n1" {
		t.Fatalf("Mode B default proxy catch-all should hit default node: %v", last)
	}
	if !strings.Contains(string(a.MosdnsConfig), "forward_fakeip") || !strings.Contains(string(a.MosdnsConfig), "exec: $forward_fakeip") {
		t.Fatal("mosdns must forward proxy domains to fakeip in Mode B")
	}
	if strings.Contains(string(a.Nft), "quic_reject") || strings.Contains(string(a.Nft), "elements") {
		t.Fatal("Mode B nft must carry no device sets and no quic reject")
	}

	// Structural hash changes between modes, but not when only a node toggles.
	b, _ := Render(baseSnap(), deps())
	if a.XrayStructuralHash == b.XrayStructuralHash {
		t.Fatal("mode change must alter structural hash")
	}
	s2 := baseSnap()
	s2.Nodes[2].Enabled = true
	c, _ := Render(s2, deps())
	if c.XrayStructuralHash != b.XrayStructuralHash {
		t.Fatal("enabling a node must not alter structural hash")
	}
	if len(c.XrayOutbounds) != 3 {
		t.Fatalf("expected 3 node outbounds, got %d", len(c.XrayOutbounds))
	}
}

func TestNoNodesAndStrict(t *testing.T) {
	s := baseSnap()
	for i := range s.Nodes {
		s.Nodes[i].Enabled = false
	}
	s.Network.Mode = model.ModeC
	a, err := Render(s, deps())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(a.Xray, &cfg)
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	for _, r := range rules {
		m := r.(map[string]any)
		if _, hasBal := m["balancerTag"]; hasBal {
			t.Fatalf("no balancer may remain without nodes: %v", m)
		}
		if m["ruleTag"] == "rule-r1" && m["outboundTag"] != "direct" {
			t.Fatalf("normal failover with no nodes should be direct: %v", m)
		}
	}
	if _, ok := cfg["observatory"]; ok {
		t.Fatal("observatory without balancers")
	}

	s.Network.NodeFailoverMode = "strict"
	a, _ = Render(s, deps())
	_ = json.Unmarshal(a.Xray, &cfg)
	for _, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
		m := r.(map[string]any)
		if m["ruleTag"] == "rule-r1" && m["outboundTag"] != "block" {
			t.Fatalf("strict failover must block: %v", m)
		}
	}
}

func TestAutoBalancerWhenNoDefault(t *testing.T) {
	s := baseSnap()
	s.Nodes[0].IsDefault = false
	a, _ := Render(s, deps())
	var cfg map[string]any
	_ = json.Unmarshal(a.Xray, &cfg)
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	for _, r := range rules {
		m := r.(map[string]any)
		if m["ruleTag"] == "rule-r5" && m["balancerTag"] != TagAutoProxy {
			t.Fatalf("proxy action without default node should use auto balancer: %v", m)
		}
	}
}

func TestMosdnsLists(t *testing.T) {
	a, err := Render(baseSnap(), deps())
	if err != nil {
		t.Fatal(err)
	}
	proxy := string(a.MosdnsProxyDomains)
	// r1 (group) youtube suffix, r4 regexp (node), r7 geosite netflix expanded; not r2 (direct), not r6 (invalid), not r8/r9.
	for _, want := range []string{"domain:youtube.com\n", "regexp:^([^.]+\\.)?example\\.org$\n", "domain:netflix.com\n", "keyword:nflx\n"} {
		if !strings.Contains(proxy, want) {
			t.Fatalf("proxy list missing %q:\n%s", want, proxy)
		}
	}
	for _, no := range []string{"baidu", "disabled.com", "inoffset.com", "bad..value"} {
		if strings.Contains(proxy, no) {
			t.Fatalf("proxy list must not contain %q", no)
		}
	}
	if string(a.MosdnsCNDomains) != "domain:baidu.com\nfull:www.qq.com\n" {
		t.Fatalf("cn list %q", a.MosdnsCNDomains)
	}
	cfg := string(a.MosdnsConfig)
	if !strings.Contains(cfg, `{ addr: "tcp://1.1.1.1", socks5: "127.0.0.1:10808", enable_pipeline: true }, { addr: "https://dns.google/dns-query", socks5: "127.0.0.1:10808", bootstrap: "223.5.5.5", idle_timeout: 30 }`) {
		t.Fatalf("remote upstreams not proxied/tcp-forced:\n%s", cfg)
	}
	if !strings.Contains(cfg, `{ addr: "119.29.29.29" }, { addr: "223.5.5.5" }`) {
		t.Fatalf("local upstreams wrong:\n%s", cfg)
	}
	if !strings.Contains(cfg, "lazy_cache_ttl: 86400") || !strings.Contains(cfg, "tcp_server") {
		t.Fatal("cache or tcp server missing")
	}

	// Without geodata the geosite rule is a warning, not an error.
	a2, err := Render(baseSnap(), Deps{Paths: paths.New("/x")})
	if err != nil {
		t.Fatal(err)
	}
	if a2.MosdnsCNDomains != nil || !strings.Contains(strings.Join(a2.Warnings, "\n"), "geosite:netflix not expanded") {
		t.Fatalf("expected no cn list and a warning: %v", a2.Warnings)
	}
}

func TestNftSets(t *testing.T) {
	a, err := Render(baseSnap(), deps())
	if err != nil {
		t.Fatal(err)
	}
	nft := string(a.Nft)
	for _, want := range []string{
		"table inet edgeroutegw\ndelete table inet edgeroutegw\ntable inet edgeroutegw {",
		"set mac_direct   { type ether_addr; elements = { aa:bb:cc:dd:ee:ff } }",
		"set ip_proxy     { type ipv4_addr; flags interval; elements = { 192.168.1.10/32 } }",
		"set ip6_reject   { type ipv6_addr; flags interval; elements = { fd00::/64 } }",
		"set bypass_dst   { type ipv4_addr; flags interval; elements = { 203.0.113.5/32 } }",
		"tproxy ip to 127.0.0.1:12345",
		"quic_reject",
		`comment "intercept_v4"`,
	} {
		if !strings.Contains(nft, want) {
			t.Fatalf("nft missing %q:\n%s", want, nft)
		}
	}
	if strings.Contains(nft, "flush ruleset") {
		t.Fatal("must never flush the whole ruleset")
	}
	// The LAN default policy is applied by Xray's catch-all, never by
	// skipping interception: domain rules and Mode B/C steering need every
	// packet in Xray.
	s := baseSnap()
	s.Network.LANDefaultPolicy = "direct"
	b, _ := Render(s, deps())
	if !strings.Contains(string(b.Nft), `comment "intercept_v4"`) || strings.Contains(string(b.Nft), "default_direct") {
		t.Fatal("direct policy must still intercept")
	}
	var cfg struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	_ = json.Unmarshal(b.Xray, &cfg)
	last := cfg.Routing.Rules[len(cfg.Routing.Rules)-1]
	if last["outboundTag"] != "direct" {
		t.Fatalf("xray catch-all must honour the direct policy: %v", last)
	}
}

func TestRoutesAndFRR(t *testing.T) {
	s := baseSnap()
	s.Network.Mode = model.ModeB
	s.Network.ServiceIface = "lan0"
	d := deps()
	d.IfaceAddr = func(iface string) (netip.Prefix, error) {
		if iface != "lan0" {
			t.Fatalf("iface %s", iface)
		}
		return netip.MustParsePrefix("192.168.10.2/24"), nil
	}
	a, err := Render(s, d)
	if err != nil {
		t.Fatal(err)
	}
	if got := RouteSet(a.Routes); len(got) != 2 || got[0] != "198.18.0.0/16" || got[1] != "8.8.8.8/32" {
		t.Fatalf("routes %v", got)
	}
	frr := string(a.FRR)
	for _, want := range []string{"ip import-table 200 distance 20", "ospf router-id 192.168.10.2", "network 192.168.10.0/24 area 0", "redistribute table 200", "no passive-interface lan0"} {
		if !strings.Contains(frr, want) {
			t.Fatalf("frr.conf missing %q:\n%s", want, frr)
		}
	}
	b, _ := Render(baseSnap(), d)
	if b.FRR != nil || len(b.Routes) != 0 {
		t.Fatal("Mode A must produce no routes and no FRR config")
	}
}

func TestFRRFallsBackToDefaultInterface(t *testing.T) {
	snap := &desired.Snapshot{Network: model.NetworkConfig{Mode: model.ModeC}}
	d := Deps{
		IfaceAddr:    func(iface string) (netip.Prefix, error) { return netip.MustParsePrefix("192.168.100.199/24"), nil },
		DefaultIface: func() (string, error) { return "eth0", nil },
	}
	a := &Artifacts{}
	if err := renderRoutes(snap, d, a); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"network 192.168.100.0/24 area 0", "no passive-interface eth0", "ospf router-id 192.168.100.199"} {
		if !strings.Contains(string(a.FRR), want) {
			t.Fatalf("missing %q in\n%s", want, a.FRR)
		}
	}
	if len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "using eth0") {
		t.Fatalf("warnings %v", a.Warnings)
	}
	// TPROXY table and route table must never coincide.
	if RouteTable == TProxyRouteTbl {
		t.Fatal("route table collides with the TPROXY table")
	}
}

func TestXrayProbeChannel(t *testing.T) {
	snap := baseSnap()
	a := &Artifacts{}
	if err := renderXray(snap, Deps{Paths: paths.New(t.TempDir())}, a); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(a.Xray, &cfg); err != nil {
		t.Fatal(err)
	}
	var probeIn map[string]any
	for _, ib := range cfg["inbounds"].([]any) {
		m := ib.(map[string]any)
		if m["tag"] == TagProbeIn {
			probeIn = m
		}
	}
	if probeIn == nil || probeIn["listen"] != "127.0.0.1" || probeIn["port"] != float64(ProbePort) {
		t.Fatalf("probe inbound %v", probeIn)
	}
	if accts := probeIn["settings"].(map[string]any)["accounts"].([]any); len(accts) != ProbeSlotCount {
		t.Fatalf("%d accounts", len(accts))
	}
	// Enabled nodes n1, n2 get slots 0 and 1 (sorted by id); n3 is disabled.
	slots := ProbeSlots(snap.Nodes)
	if slots["n1"] != 0 || slots["n2"] != 1 {
		t.Fatalf("slots %v", slots)
	}
	if _, ok := slots["n3"]; ok {
		t.Fatal("disabled node must not get a slot")
	}
	var rules []map[string]any
	json.Unmarshal(a.XrayRules, &rules)
	want := map[string]string{"probe-n1": "node-n1", "probe-n2": "node-n2", "probe-none": TagBlock}
	seenCatch := false
	for i, r := range rules {
		if tag, _ := r["ruleTag"].(string); want[tag] != "" {
			if r["outboundTag"] != want[tag] {
				t.Fatalf("rule %s -> %v", tag, r["outboundTag"])
			}
			if ibs := r["inboundTag"].([]any); len(ibs) != 1 || ibs[0] != TagProbeIn {
				t.Fatalf("rule %s inboundTag %v", tag, r["inboundTag"])
			}
			delete(want, tag)
			if tag == "probe-none" {
				seenCatch = true
			}
		} else if seenCatch && i < len(rules)-1 {
			// user rules may follow; fine
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing probe rules %v", want)
	}
	// Probe rules precede the first user rule.
	firstUser, lastProbe := -1, -1
	for i, r := range rules {
		tag, _ := r["ruleTag"].(string)
		switch {
		case strings.HasPrefix(tag, "probe-"):
			lastProbe = i
		case tag != RuleTagAPI && tag != RuleTagDNS && firstUser < 0:
			firstUser = i
		}
	}
	if firstUser >= 0 && lastProbe > firstUser {
		t.Fatalf("probe rules must come before user rules (probe %d, user %d)", lastProbe, firstUser)
	}
}

func TestKernelWireGuardOutbound(t *testing.T) {
	snap := baseSnap()
	wg := model.Node{ID: "wg1", Name: "wg", Protocol: model.ProtocolWireGuard, Address: "203.0.113.9", Port: 51820, Enabled: true, Credential: "PRIVATE",
		Params: `{"settings":{"address":["10.77.0.2/32"],"mtu":1380,"peers":[{"publicKey":"PUB","endpoint":"203.0.113.9:51820","allowedIPs":["0.0.0.0/0","::/0"]}]}}`}
	warp := wg
	warp.ID, warp.Name = "wg2", "warp"
	warp.Params = `{"settings":{"address":["10.77.0.3/32"],"reserved":[1,2,3],"peers":[{"publicKey":"PUB","endpoint":"203.0.113.9:51820"}]}}`
	snap.Nodes = append(snap.Nodes, wg, warp)

	d := deps()
	d.KernelWG = true
	a, err := Render(snap, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.WG) != 1 || a.WG[0].NodeID != "wg1" || a.WG[0].Name != WGIfaceName("wg1") || a.WG[0].Table != WGTableBase || a.WG[0].Mark != WGMarkBase || a.WG[0].MTU != 1380 || a.WG[0].PrivateKey != "PRIVATE" {
		t.Fatalf("wg ifaces %+v", a.WG)
	}
	if a.WG[0].Peers[0].Keepalive != WGKeepalive || a.WG[0].Peers[0].Endpoint != "203.0.113.9:51820" {
		t.Fatalf("peer %+v", a.WG[0].Peers[0])
	}
	var ob map[string]any
	_ = json.Unmarshal(a.XrayOutbounds["node-wg1"], &ob)
	if ob["protocol"] != "freedom" || ob["streamSettings"].(map[string]any)["sockopt"].(map[string]any)["mark"] != float64(WGMarkBase) {
		t.Fatalf("kernel node outbound %v", ob)
	}
	_ = json.Unmarshal(a.XrayOutbounds["node-wg2"], &ob)
	if ob["protocol"] != "wireguard" {
		t.Fatalf("warp node must stay userspace: %v", ob)
	}
	found := false
	for _, w := range a.Warnings {
		if strings.Contains(w, "warp") && strings.Contains(w, "userspace") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected fallback warning, got %v", a.Warnings)
	}
	// Without the module nothing changes.
	d.KernelWG = false
	a, _ = Render(snap, d)
	_ = json.Unmarshal(a.XrayOutbounds["node-wg1"], &ob)
	if len(a.WG) != 0 || ob["protocol"] != "wireguard" {
		t.Fatalf("userspace expected without kernel module: %v", ob)
	}
	if !strings.Contains(string(a.Nft), `comment "mss_clamp"`) || !strings.Contains(string(a.Nft), "hook forward") {
		t.Fatal("forward chain with MSS clamp missing")
	}
}

func TestNftDNSHijack(t *testing.T) {
	snap := baseSnap()
	snap.DNS.Hijack, snap.DNS.BlockDoT = true, true
	a, _ := Render(snap, deps())
	nft := string(a.Nft)
	for _, want := range []string{`comment "dns_to_hijack"`, "type nat hook prerouting priority dstnat", `redirect to :53 comment "dns_hijack"`, `comment "dot_reject"`} {
		if !strings.Contains(nft, want) {
			t.Fatalf("missing %q", want)
		}
	}
	// The exclusion for DNS must come before interception.
	if strings.Index(nft, "dns_to_hijack") > strings.Index(nft, "intercept_v4") {
		t.Fatal("dns_to_hijack must precede intercept rules")
	}
	snap.DNS.Hijack, snap.DNS.BlockDoT = false, false
	a, _ = Render(snap, deps())
	if strings.Contains(string(a.Nft), "dns_hijack") || strings.Contains(string(a.Nft), "dot_reject") {
		t.Fatal("hijack rules rendered while disabled")
	}
	if !strings.Contains(string(a.MosdnsConfig), `listen: ":53"`) {
		t.Fatal("mosdns must listen dual-stack")
	}
}

func TestMosdnsEncryptedUpstreams(t *testing.T) {
	out := upstreams([]string{"https://223.5.5.5/dns-query", "tls://1.12.12.12", "119.29.29.29"}, false)
	for _, want := range []string{`addr: "https://223.5.5.5/dns-query", idle_timeout: 30`, `addr: "tls://1.12.12.12", idle_timeout: 30, enable_pipeline: true`, `{ addr: "119.29.29.29" }`} {
		if !strings.Contains(out, want) {
			t.Fatalf("local: missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "socks5") || strings.Contains(out, "bootstrap") {
		t.Fatalf("local upstreams must not be proxied or bootstrapped: %s", out)
	}
	out = upstreams([]string{"https://1.1.1.1/dns-query", "tls://8.8.8.8", "9.9.9.9", "https://dns.quad9.net/dns-query"}, true)
	for _, want := range []string{`addr: "https://1.1.1.1/dns-query", socks5: "127.0.0.1:10808", idle_timeout: 30`, `addr: "tls://8.8.8.8", socks5: "127.0.0.1:10808"`, `addr: "tcp://9.9.9.9", socks5: "127.0.0.1:10808", enable_pipeline: true`, `addr: "https://dns.quad9.net/dns-query", socks5: "127.0.0.1:10808", bootstrap: "223.5.5.5"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("remote: missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "tcp://https") {
		t.Fatalf("encrypted scheme must survive forceTCP: %s", out)
	}
	if d := upstreams(nil, false); !strings.Contains(d, "https://223.5.5.5/dns-query") || !strings.Contains(d, "tls://1.12.12.12") {
		t.Fatalf("defaults %s", d)
	}
}

func TestRemoteDNSUpstreamsPinnedToProxyAndModeAFallback(t *testing.T) {
	snap := baseSnap()
	snap.DNS.RemoteUpstreams = []string{"https://1.1.1.1/dns-query", "tls://8.8.8.8", "https://dns.quad9.net/dns-query", "10.0.0.53"}
	snap.Network.LANDefaultPolicy = "direct"
	a, err := Render(snap, deps())
	if err != nil {
		t.Fatal(err)
	}
	var rules []map[string]any
	_ = json.Unmarshal(a.XrayRules, &rules)
	pinned := 0
	for _, r := range rules {
		tag, _ := r["ruleTag"].(string)
		if !strings.HasPrefix(tag, "sys-dns-upstream-") {
			continue
		}
		pinned++
		if ibs := r["inboundTag"].([]any); ibs[0] != TagSocksIn {
			t.Fatalf("rule %s inbound %v", tag, ibs)
		}
		if r["outboundTag"] == TagDirect || r["outboundTag"] == TagBlock {
			t.Fatalf("rule %s must go to the proxy, got %v", tag, r["outboundTag"])
		}
	}
	if pinned != 3 { // the private 10.0.0.53 is not pinned
		t.Fatalf("pinned %d upstream rules", pinned)
	}
	last := rules[len(rules)-1]
	if last["outboundTag"] != TagDirect {
		t.Fatalf("direct fallback: %v", last)
	}
	// Mode A honours a proxy fallback too (it used to be forced direct).
	snap.Network.LANDefaultPolicy = "proxy"
	a, _ = Render(snap, deps())
	_ = json.Unmarshal(a.XrayRules, &rules)
	last = rules[len(rules)-1]
	if last["outboundTag"] == TagDirect {
		t.Fatalf("Mode A with proxy fallback must not be direct: %v", last)
	}
}

func TestMosdnsECSForwardAndIPv6(t *testing.T) {
	snap := baseSnap()
	snap.DNS.ECSSubnet = "120.197.0.0/16"
	snap.DNS.ForwardRules = []model.DNSForwardRule{{Suffix: "corp.local", Upstream: "10.100.10.10"}}
	snap.DNS.IPv6Mode = "proxy"
	a, err := Render(snap, deps())
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(a.MosdnsConfig)
	for _, want := range []string{
		`type: ecs_handler`, `preset: "120.197.0.0", mask4: 16`, "- exec: $ecs_local\n      - exec: $forward_local", "exec: $local_seq",
		`tag: forward_internal_0`, `{ addr: "10.100.10.10" }`, "matches: [ qname domain:corp.local ]\n        exec: $forward_internal_0",
		"matches: [ qname $proxy_domain, qtype 28 ]\n        exec: reject 0",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("missing %q in\n%s", want, cfg)
		}
	}
	if strings.Index(cfg, "forward_internal_0\n") > strings.Index(cfg, "$lazy_cache") && strings.Contains(cfg, "$lazy_cache") {
		t.Fatal("internal forwarding must run before the cache")
	}
	snap.DNS.IPv6Mode = "all"
	snap.DNS.ECSSubnet = ""
	a, _ = Render(snap, deps())
	cfg = string(a.MosdnsConfig)
	if !strings.Contains(cfg, "matches: [ qtype 28 ]\n        exec: reject 0") || strings.Contains(cfg, "ecs_handler") {
		t.Fatalf("ipv6 all / no ecs:\n%s", cfg)
	}
}

func TestLoadBalanceGroupHasFallback(t *testing.T) {
	snap := baseSnap()
	// A load-balancing group over two nodes that are not the default node.
	a := snap.Nodes[0]
	b := a
	b.ID, b.Name, b.IsDefault = "n-b", "B", false
	c := a
	c.ID, c.Name, c.IsDefault = "n-c", "C", false
	snap.Nodes = append(snap.Nodes, b, c)
	snap.Groups = append(snap.Groups, model.OutboundGroup{ID: "g-lb", Name: "LB", Kind: model.GroupLoadBalance, MemberIDs: []string{"n-b", "n-c"}, Strategy: `{"strategy":"leastPing"}`})
	snap.Rules = append(snap.Rules, model.Rule{ID: "r-lb", SetID: snap.RuleSets[0].ID, MatchKind: model.MatchGeosite, MatchValue: "cn", ActionKind: model.ActionGroup, ActionRef: "g-lb", Enabled: true})
	for _, tc := range []struct{ mode, want string }{{"normal", NodeTag(a.ID)}, {"strict", TagBlock}} {
		snap.Network.NodeFailoverMode = tc.mode
		art, err := Render(snap, deps())
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		_ = json.Unmarshal(art.Xray, &cfg)
		found := false
		for _, raw := range cfg["routing"].(map[string]any)["balancers"].([]any) {
			bl := raw.(map[string]any)
			if bl["tag"] == GroupTag("g-lb") {
				found = true
				if bl["fallbackTag"] != tc.want {
					t.Fatalf("%s: fallbackTag %v, want %s", tc.mode, bl["fallbackTag"], tc.want)
				}
			}
		}
		if !found {
			t.Fatalf("%s: balancer for g-lb not rendered", tc.mode)
		}
	}
}
