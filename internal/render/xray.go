package render

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/rulespec"
)

// Fixed tags. Changing any of these is a config-structure change that forces
// an Xray restart, so they are constants rather than configuration.
const (
	TagTProxyIn = "tproxy_in"
	TagAPIIn    = "api_inbound"
	TagSocksIn  = "socks_in"
	// TagProbeIn is a loopback SOCKS inbound with one account per probe
	// slot; routing rules map each account to one node outbound so the
	// latency prober can test every node end to end (WireGuard included).
	TagProbeIn    = "probe_in"
	TagHTTPIn     = "http_in"
	TagDNSIn      = "dns-in"
	TagDirect     = "direct"
	TagBlock      = "block"
	TagDNSOut     = "dns-out"
	TagAPI        = "api"
	TagAutoProxy  = "proxy-auto" // leastPing balancer over every enabled node
	RuleTagAPI    = "sys-api"
	RuleTagDNS    = "sys-dns"
	RuleTagCatch  = "default"
	APIListenAddr = "127.0.0.1:10085"
	SocksPort     = 10808
	ProbePort     = 10810
	// ProbeSlotCount bounds the fixed account list so the inbound never
	// changes (inbound changes force an Xray restart; rules hot-update).
	ProbeSlotCount = 64
	ProbePassword  = "probe"
	HTTPPort       = 10809
	FakeDNSPort    = 5353
	OutboundMark   = 2
)

// NodeTag is the outbound tag for a node.
func NodeTag(id string) string { return "node-" + id }

// GroupTag is the balancer tag for an outbound group.
func GroupTag(id string) string { return "group-" + id }

// RuleTag is the ruleTag for a rule.
func RuleTag(id string) string { return "rule-" + id }

// target is where a rule sends traffic: exactly one of the two is set.
type target struct {
	outbound string
	balancer string
}

func (t target) apply(rule map[string]any) {
	if t.balancer != "" {
		rule["balancerTag"] = t.balancer
		return
	}
	rule["outboundTag"] = t.outbound
}

type xrayRenderer struct {
	snap     *desired.Snapshot
	art      *Artifacts
	liveTags map[string]bool // enabled node outbound tags
	strict   bool
	// balancers collected while resolving rules; keyed by tag for dedupe.
	balancers map[string]map[string]any
}

func renderXray(snap *desired.Snapshot, d Deps, a *Artifacts) error {
	r := &xrayRenderer{
		snap:      snap,
		art:       a,
		liveTags:  map[string]bool{},
		strict:    snap.Network.NodeFailoverMode == "strict",
		balancers: map[string]map[string]any{},
	}
	mode := snap.Network.Mode
	cfg := r.base(d, mode)

	// Outbounds: direct, block, (dns-out), then one per enabled node.
	outbounds := cfg["outbounds"].([]any)
	nodeOutbounds := map[string]json.RawMessage{}
	kernelWG := map[string]WGIface{}
	for _, w := range a.WG {
		kernelWG[w.NodeID] = w
	}
	for _, n := range snap.EnabledNodes() {
		var ob map[string]any
		var err error
		if w, ok := kernelWG[n.ID]; ok {
			ob = wgOutbound(n.ID, w)
		} else {
			ob, err = buildOutbound(n)
		}
		if err != nil {
			a.warn("node %q skipped: %v", n.Name, err)
			continue
		}
		raw, err := json.Marshal(ob)
		if err != nil {
			return err
		}
		tag := NodeTag(n.ID)
		r.liveTags[tag] = true
		nodeOutbounds[tag] = raw
		outbounds = append(outbounds, ob)
	}
	cfg["outbounds"] = outbounds

	// Rules.
	rules := []map[string]any{{"type": "field", "inboundTag": []string{TagAPIIn}, "outboundTag": TagAPI, "ruleTag": RuleTagAPI}}
	if mode == model.ModeB {
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{TagDNSIn}, "outboundTag": TagDNSOut, "ruleTag": RuleTagDNS})
	}
	// Probe channel: account probe-<slot> -> that node; anything else on the
	// probe inbound is dropped. Restricted by inboundTag, so LAN traffic is
	// never affected.
	slots := ProbeSlots(snap.Nodes)
	bySlot := make([]string, ProbeSlotCount)
	for id, slot := range slots {
		bySlot[slot] = id
	}
	for slot, id := range bySlot {
		if id == "" {
			continue
		}
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{TagProbeIn}, "user": []string{ProbeUser(slot)}, "outboundTag": "node-" + id, "ruleTag": "probe-" + id})
	}
	rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{TagProbeIn}, "outboundTag": TagBlock, "ruleTag": "probe-none"})
	// Mosdns reaches the remote (public) upstreams through the local SOCKS
	// inbound; pin those flows to the proxy so the queries really leave
	// through a node whatever the user rules and the fallback say.
	for i, up := range snap.DNS.RemoteUpstreams {
		norm, err := rulespec.NormalizeUpstream(up)
		if err != nil {
			continue
		}
		host := rulespec.UpstreamHost(norm)
		if host == "" || !rulespec.IsPublicAddr(host) {
			continue
		}
		xr := map[string]any{"type": "field", "inboundTag": []string{TagSocksIn}, "ruleTag": fmt.Sprintf("sys-dns-upstream-%d", i)}
		if net.ParseIP(host) != nil {
			xr["ip"] = []string{host}
		} else {
			xr["domain"] = []string{"full:" + host}
		}
		r.defaultTarget().apply(xr)
		rules = append(rules, xr)
	}
	for _, rule := range snap.ActiveRules() {
		xr, ok := r.rule(rule)
		if ok {
			rules = append(rules, xr)
		}
	}
	catch := map[string]any{"type": "field", "network": "tcp,udp", "ruleTag": RuleTagCatch}
	r.catchAll().apply(catch)
	rules = append(rules, catch)

	routing := cfg["routing"].(map[string]any)
	routing["rules"] = rules
	if len(r.balancers) > 0 {
		tags := make([]string, 0, len(r.balancers))
		for t := range r.balancers {
			tags = append(tags, t)
		}
		sort.Strings(tags)
		bl := make([]any, 0, len(tags))
		for _, t := range tags {
			bl = append(bl, r.balancers[t])
		}
		routing["balancers"] = bl
		cfg["observatory"] = map[string]any{
			"subjectSelector": []string{"node-"},
			"probeURL":        "http://cp.cloudflare.com/",
			"probeInterval":   "30s",
		}
	}

	full, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	full = append(full, '\n')
	a.Xray = full
	a.XrayOutbounds = nodeOutbounds
	if a.XrayRules, err = json.Marshal(rules); err != nil {
		return err
	}
	// Structural hash: the config minus the two hot-updatable parts.
	structural := map[string]any{}
	for k, v := range cfg {
		if k != "outbounds" && k != "routing" {
			structural[k] = v
		}
	}
	structural["routing"] = map[string]any{
		"domainStrategy": routing["domainStrategy"],
		"balancers":      routing["balancers"],
	}
	// Direct/block/dns-out outbounds are structural; node outbounds are not.
	var fixedOut []any
	for _, ob := range outbounds {
		if m, ok := ob.(map[string]any); ok {
			if tag, _ := m["tag"].(string); !strings.HasPrefix(tag, "node-") {
				fixedOut = append(fixedOut, m)
			}
		}
	}
	structural["outbounds"] = fixedOut
	sb, err := json.Marshal(structural)
	if err != nil {
		return err
	}
	a.XrayStructuralHash = hashBytes(sb)
	return nil
}

func (r *xrayRenderer) base(d Deps, mode model.Mode) map[string]any {
	sniff := func(extra ...string) map[string]any {
		return map[string]any{"enabled": true, "destOverride": append([]string{"http", "tls", "quic"}, extra...), "routeOnly": true}
	}
	tproxySniff := sniff()
	if mode == model.ModeB {
		tproxySniff = sniff("fakedns")
	}
	// Sniff override (Mode A/C): the sniffed domain replaces the destination,
	// so logs and the observe page carry names; direct flows are then
	// resolved by Xray itself, through Mosdns, to keep answers consistent.
	override := r.snap.Network.SniffOverride && mode != model.ModeB
	if override {
		tproxySniff["routeOnly"] = false
	}
	port := r.snap.Network.TProxyPort
	if port <= 0 {
		port = 12345
	}
	inbounds := []any{
		map[string]any{
			"tag": TagTProxyIn, "port": port, "listen": "::", "protocol": "dokodemo-door",
			"settings":       map[string]any{"network": "tcp,udp", "followRedirect": true},
			"streamSettings": map[string]any{"sockopt": map[string]any{"tproxy": "tproxy"}},
			"sniffing":       tproxySniff,
		},
		map[string]any{
			"tag": TagAPIIn, "listen": "127.0.0.1", "port": 10085, "protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1"},
		},
		map[string]any{
			"tag": TagSocksIn, "listen": "127.0.0.1", "port": SocksPort, "protocol": "socks",
			"settings": map[string]any{"udp": true, "auth": "noauth"}, "sniffing": sniff(),
		},
		map[string]any{
			"tag": TagHTTPIn, "listen": "127.0.0.1", "port": HTTPPort, "protocol": "http",
			"settings": map[string]any{"allowTransparent": false}, "sniffing": sniff(),
		},
		map[string]any{
			"tag": TagProbeIn, "listen": "127.0.0.1", "port": ProbePort, "protocol": "socks",
			"settings": map[string]any{"udp": false, "auth": "password", "accounts": probeAccounts()},
		},
	}
	direct := map[string]any{"tag": TagDirect, "protocol": "freedom", "streamSettings": map[string]any{"sockopt": map[string]any{"mark": OutboundMark}}}
	if override {
		direct["settings"] = map[string]any{"domainStrategy": "UseIP"}
	}
	outbounds := []any{
		direct,
		map[string]any{"tag": TagBlock, "protocol": "blackhole"},
	}
	cfg := map[string]any{
		"log":   map[string]any{"loglevel": "warning", "access": d.Paths.XrayAccessLog()},
		"api":   map[string]any{"tag": TagAPI, "services": []string{"StatsService", "RoutingService", "HandlerService"}},
		"stats": map[string]any{},
		"policy": map[string]any{"system": map[string]any{
			"statsInboundDownlink": true, "statsInboundUplink": true,
			"statsOutboundDownlink": true, "statsOutboundUplink": true,
		}},
		"routing": map[string]any{"domainStrategy": "IPIfNonMatch"},
	}
	if override {
		cfg["dns"] = map[string]any{"servers": []any{"127.0.0.1"}, "queryStrategy": "UseIPv4", "disableFallback": true}
		// Xray logs "domain X will use the first DNS" at warning level for
		// every sniffed connection; that would flood the journal.
		cfg["log"].(map[string]any)["loglevel"] = "error"
	}
	if mode == model.ModeB {
		pool := r.snap.Network.FakeIPPool
		if pool == "" {
			pool = "198.18.0.0/16"
		}
		cfg["fakedns"] = []any{map[string]any{"id": "fakedns", "ipPool": pool, "poolSize": 65535}}
		cfg["dns"] = map[string]any{"servers": []string{"fakedns"}}
		inbounds = append(inbounds, map[string]any{
			"tag": TagDNSIn, "listen": "127.0.0.1", "port": FakeDNSPort, "protocol": "dokodemo-door",
			"settings": map[string]any{"address": "8.8.8.8", "port": 53, "network": "udp"},
		})
		outbounds = append(outbounds, map[string]any{"tag": TagDNSOut, "protocol": "dns", "streamSettings": map[string]any{"sockopt": map[string]any{"mark": OutboundMark}}})
	}
	cfg["inbounds"] = inbounds
	cfg["outbounds"] = outbounds
	return cfg
}

// defaultTarget is where "proxy" traffic goes: the default node if one is
// enabled, else a leastPing balancer over all enabled nodes, else direct.
func (r *xrayRenderer) defaultTarget() target {
	if n := r.snap.DefaultNode(); n != nil {
		return target{outbound: NodeTag(n.ID)}
	}
	if len(r.liveTags) == 0 {
		return target{outbound: TagDirect}
	}
	r.balancers[TagAutoProxy] = map[string]any{
		"tag": TagAutoProxy, "selector": []string{"node-"},
		"strategy": map[string]any{"type": "leastPing"},
	}
	return target{balancer: TagAutoProxy}
}

// missingTarget is the policy for a rule whose node/group has no live member:
// strict fails closed, normal falls back to the default proxy path.
func (r *xrayRenderer) missingTarget(what string) target {
	if r.strict {
		r.art.warn("%s has no enabled node; strict failover routes it to block", what)
		return target{outbound: TagBlock}
	}
	r.art.warn("%s has no enabled node; routing via default outbound", what)
	return r.defaultTarget()
}

// catchAll is the outbound for traffic no rule matched: the LAN default
// policy, in every mode (nft intercepts everything, so this is the only
// place the policy takes effect).
func (r *xrayRenderer) catchAll() target {
	if r.snap.Network.LANDefaultPolicy == "proxy" {
		return r.defaultTarget()
	}
	return target{outbound: TagDirect}
}

func (r *xrayRenderer) resolveAction(rule model.Rule) target {
	switch rule.ActionKind {
	case model.ActionDirect:
		return target{outbound: TagDirect}
	case model.ActionBlock:
		return target{outbound: TagBlock}
	case model.ActionNode:
		tag := NodeTag(rule.ActionRef)
		if r.liveTags[tag] {
			return target{outbound: tag}
		}
		return r.missingTarget(fmt.Sprintf("rule %s (node %s)", rule.MatchValue, rule.ActionRef))
	case model.ActionGroup:
		return r.groupTarget(rule)
	default:
		return r.defaultTarget()
	}
}

func (r *xrayRenderer) groupTarget(rule model.Rule) target {
	g := r.snap.Group(rule.ActionRef)
	if g == nil {
		return r.missingTarget(fmt.Sprintf("rule %s (group %s missing)", rule.MatchValue, rule.ActionRef))
	}
	var live []string
	for _, id := range g.MemberIDs {
		if t := NodeTag(id); r.liveTags[t] {
			live = append(live, t)
		}
	}
	switch len(live) {
	case 0:
		return r.missingTarget(fmt.Sprintf("group %q", g.Name))
	case 1:
		if len(g.MemberIDs) > 1 {
			r.art.warn("group %q has one enabled member; routing directly to it", g.Name)
		}
		return target{outbound: live[0]}
	}
	tag := GroupTag(g.ID)
	if _, done := r.balancers[tag]; done {
		return target{balancer: tag}
	}
	switch g.Kind {
	case model.GroupFailover:
		b := map[string]any{"tag": tag, "selector": []string{live[0]}, "fallbackTag": live[1]}
		if len(live) > 2 {
			r.art.warn("group %q: failover uses the first two enabled members; %d more ignored", g.Name, len(live)-2)
		}
		r.balancers[tag] = b
	default:
		strategy := "leastPing"
		var st struct {
			Strategy string `json:"strategy"`
		}
		if json.Unmarshal([]byte(g.Strategy), &st) == nil && st.Strategy != "" {
			strategy = st.Strategy
		}
		r.balancers[tag] = map[string]any{"tag": tag, "selector": live, "strategy": map[string]any{"type": strategy}}
	}
	return target{balancer: tag}
}

func (r *xrayRenderer) rule(rule model.Rule) (map[string]any, bool) {
	xr := map[string]any{"type": "field", "ruleTag": RuleTag(rule.ID)}
	switch rule.MatchKind {
	case model.MatchDomain:
		p, err := rulespec.ParseDomain(rule.MatchValue)
		if err != nil {
			r.art.warn("rule %s skipped: %v", rule.ID, err)
			return nil, false
		}
		xr["domain"] = []string{p.XrayValue()}
	case model.MatchGeosite:
		tag, err := rulespec.NormalizeGeoTag(rule.MatchValue)
		if err != nil {
			r.art.warn("rule %s skipped: %v", rule.ID, err)
			return nil, false
		}
		xr["domain"] = []string{"geosite:" + tag}
	case model.MatchIP:
		cidr, err := rulespec.NormalizeIPOrCIDR(rule.MatchValue)
		if err != nil {
			r.art.warn("rule %s skipped: %v", rule.ID, err)
			return nil, false
		}
		xr["ip"] = []string{cidr}
	case model.MatchGeoIP:
		tag, err := rulespec.NormalizeGeoTag(rule.MatchValue)
		if err != nil {
			r.art.warn("rule %s skipped: %v", rule.ID, err)
			return nil, false
		}
		if tag == "private" {
			xr["ip"] = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10"}
		} else {
			xr["ip"] = []string{"geoip:" + tag}
		}
	default:
		r.art.warn("rule %s skipped: unknown match kind %q", rule.ID, rule.MatchKind)
		return nil, false
	}
	r.resolveAction(rule).apply(xr)
	return xr, true
}

// shorthandKeys are the flat node parameters the UI/importer may store; they
// are folded into settings/streamSettings and must not leak into the outbound.
var shorthandKeys = []string{"flow", "type", "security", "sni", "fp", "pbk", "sid", "method", "encryption", "alterId", "headerType", "spx"}

// buildOutbound converts a node into an Xray outbound object. Params may be a
// full outbound fragment (settings/streamSettings/...), shorthand keys, or
// both; the credential is injected into the protocol's user/server slot.
func buildOutbound(n model.Node) (map[string]any, error) {
	ob := map[string]any{}
	if strings.TrimSpace(n.Params) != "" {
		if err := json.Unmarshal([]byte(n.Params), &ob); err != nil {
			return nil, fmt.Errorf("params is not a JSON object: %w", err)
		}
	}
	str := func(k string) string { v, _ := ob[k].(string); return v }
	proto := strings.ToLower(string(n.Protocol))

	if _, has := ob["settings"]; !has {
		switch proto {
		case "vmess":
			user := map[string]any{"id": n.Credential, "alterId": 0}
			if s := str("security"); s != "" && isVmessCipher(s) {
				user["security"] = s
			}
			ob["settings"] = map[string]any{"vnext": []any{map[string]any{"address": n.Address, "port": n.Port, "users": []any{user}}}}
		case "vless":
			user := map[string]any{"id": n.Credential, "encryption": "none"}
			if e := str("encryption"); e != "" {
				user["encryption"] = e
			}
			if f := str("flow"); f != "" {
				user["flow"] = f
			}
			ob["settings"] = map[string]any{"vnext": []any{map[string]any{"address": n.Address, "port": n.Port, "users": []any{user}}}}
		case "trojan":
			ob["settings"] = map[string]any{"servers": []any{map[string]any{"address": n.Address, "port": n.Port, "password": n.Credential}}}
		case "shadowsocks":
			method := str("method")
			if method == "" {
				method = "aes-256-gcm"
			}
			ob["settings"] = map[string]any{"servers": []any{map[string]any{"address": n.Address, "port": n.Port, "password": n.Credential, "method": method}}}
		case "socks", "http":
			srv := map[string]any{"address": n.Address, "port": n.Port}
			if user, pass, ok := strings.Cut(n.Credential, ":"); ok && user != "" {
				srv["users"] = []any{map[string]any{"user": user, "pass": pass}}
			}
			ob["settings"] = map[string]any{"servers": []any{srv}}
		case "wireguard":
			return nil, fmt.Errorf("wireguard node needs params.settings (secretKey is taken from the credential)")
		case "custom":
			return nil, fmt.Errorf("custom node needs params.settings")
		default:
			return nil, fmt.Errorf("unsupported protocol %q", proto)
		}
	} else {
		settings, ok := ob["settings"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("params.settings must be an object")
		}
		injectEndpoint(settings, n)
		if proto == "wireguard" && n.Credential != "" {
			settings["secretKey"] = n.Credential
		}
	}

	// Shorthand transport -> streamSettings (importer output and UI form).
	if _, has := ob["streamSettings"]; !has && str("type") != "" {
		ss := map[string]any{"network": str("type")}
		if sec := str("security"); sec != "" && sec != "none" {
			ss["security"] = sec
			switch sec {
			case "reality":
				rs := map[string]any{"serverName": str("sni"), "publicKey": str("pbk"), "shortId": str("sid"), "spiderX": "/"}
				if fp := str("fp"); fp != "" {
					rs["fingerprint"] = fp
				}
				if spx := str("spx"); spx != "" {
					rs["spiderX"] = spx
				}
				ss["realitySettings"] = rs
			case "tls":
				ts := map[string]any{"serverName": str("sni")}
				if fp := str("fp"); fp != "" {
					ts["fingerprint"] = fp
				}
				ss["tlsSettings"] = ts
			}
		}
		ob["streamSettings"] = ss
	}
	for _, k := range shorthandKeys {
		delete(ob, k)
	}

	ss, _ := ob["streamSettings"].(map[string]any)
	if ss == nil {
		ss = map[string]any{}
		ob["streamSettings"] = ss
	}
	ss["sockopt"] = map[string]any{"mark": OutboundMark}

	ob["protocol"] = proto
	ob["tag"] = NodeTag(n.ID)
	return ob, nil
}

func isVmessCipher(s string) bool {
	switch s {
	case "auto", "aes-128-gcm", "chacha20-poly1305", "none", "zero":
		return true
	}
	return false
}

// injectEndpoint sets address/port on vnext[0] or servers[0] when present.
func injectEndpoint(settings map[string]any, n model.Node) {
	for _, key := range []string{"vnext", "servers"} {
		list, ok := settings[key].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		if first, ok := list[0].(map[string]any); ok {
			first["address"] = n.Address
			first["port"] = n.Port
		}
	}
}

// ProbeUser is the SOCKS account name of a probe slot.
func ProbeUser(slot int) string { return fmt.Sprintf("probe-%d", slot) }

// ProbeSlots assigns probe slots to enabled nodes: sorted by ID so the
// renderer and the prober agree without talking to each other. Nodes past
// ProbeSlotCount get no slot (and fall back to a TCP probe).
func ProbeSlots(nodes []model.Node) map[string]int {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Enabled {
			ids = append(ids, n.ID)
		}
	}
	sort.Strings(ids)
	out := make(map[string]int, len(ids))
	for i, id := range ids {
		if i >= ProbeSlotCount {
			break
		}
		out[id] = i
	}
	return out
}

func probeAccounts() []any {
	out := make([]any, 0, ProbeSlotCount)
	for i := 0; i < ProbeSlotCount; i++ {
		out = append(out, map[string]any{"user": ProbeUser(i), "pass": ProbePassword})
	}
	return out
}
