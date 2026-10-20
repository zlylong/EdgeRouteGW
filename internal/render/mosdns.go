package render

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/rulespec"
)

func renderMosdns(snap *desired.Snapshot, d Deps, a *Artifacts) error {
	dns := snap.DNS
	logLevel := dns.LogLevel
	switch logLevel {
	case "debug", "info", "warn", "error":
	default:
		logLevel = "info"
	}
	cacheSize, lazyTTL := dns.CacheSize, dns.LazyCacheTTL
	if cacheSize <= 0 {
		cacheSize = 10240
	}
	if lazyTTL <= 0 {
		lazyTTL = 86400
	}

	var b strings.Builder
	fmt.Fprintf(&b, "log:\n  level: %s\n  file: %s\nplugins:\n", logLevel, yamlQuote(d.Paths.MosdnsLog()))
	fmt.Fprintf(&b, "  - tag: proxy_domain\n    type: domain_set\n    args:\n      files:\n        - %s\n", yamlQuote(d.Paths.MosdnsProxyList()))
	fmt.Fprintf(&b, "  - tag: geosite_cn\n    type: domain_set\n    args:\n      files:\n        - %s\n", yamlQuote(d.Paths.MosdnsCNList()))
	if dns.LazyCache {
		fmt.Fprintf(&b, "  - tag: lazy_cache\n    type: cache\n    args: { size: %d, lazy_cache_ttl: %d }\n", cacheSize, lazyTTL)
	}
	fmt.Fprintf(&b, "  - tag: forward_local\n    type: forward\n    args: { upstreams: %s }\n", upstreams(dns.LocalUpstreams, false))
	fmt.Fprintf(&b, "  - tag: forward_remote\n    type: forward\n    args: { upstreams: %s }\n", upstreams(dns.RemoteUpstreams, true))
	proxyExec := "$forward_remote"
	if snap.Network.Mode == model.ModeB {
		fmt.Fprintf(&b, "  - tag: forward_fakeip\n    type: forward\n    args: { upstreams: [{ addr: \"127.0.0.1:%d\" }] }\n", FakeDNSPort)
		proxyExec = "$forward_fakeip"
	}
	b.WriteString("  - tag: main_sequence\n    type: sequence\n    args:\n")
	if dns.LazyCache {
		b.WriteString("      - exec: $lazy_cache\n      - matches: [ has_resp ]\n        exec: return\n")
	}
	fmt.Fprintf(&b, "      - matches: [ qname $proxy_domain ]\n        exec: %s\n      - matches: [ has_resp ]\n        exec: return\n", proxyExec)
	b.WriteString("      - matches: [ qname $geosite_cn ]\n        exec: $forward_local\n      - matches: [ has_resp ]\n        exec: return\n")
	b.WriteString("      - exec: $forward_remote\n")
	b.WriteString("  - tag: udp_server\n    type: udp_server\n    args:\n      entry: main_sequence\n      listen: 0.0.0.0:53\n")
	b.WriteString("  - tag: tcp_server\n    type: tcp_server\n    args:\n      entry: main_sequence\n      listen: 0.0.0.0:53\n")
	a.MosdnsConfig = []byte(b.String())

	// proxy_domains: every domain/geosite rule whose action sends traffic to
	// a proxy (default, node or group). Direct/block rules resolve locally or
	// via geosite_cn like any other name.
	seen := map[string]bool{}
	var list []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			list = append(list, v)
		}
	}
	for _, rule := range snap.ActiveRules() {
		switch rule.ActionKind {
		case model.ActionProxy, model.ActionNode, model.ActionGroup:
		default:
			continue
		}
		switch rule.MatchKind {
		case model.MatchDomain:
			if p, err := rulespec.ParseDomain(rule.MatchValue); err == nil {
				add(p.MosdnsValue())
			}
		case model.MatchGeosite:
			tag, err := rulespec.NormalizeGeoTag(rule.MatchValue)
			if err != nil {
				continue
			}
			if d.GeoSite == nil {
				a.warn("geosite:%s not expanded for DNS: no geodata installed", tag)
				continue
			}
			entries, err := d.GeoSite.Entries(tag)
			if err != nil {
				a.warn("geosite:%s not expanded for DNS: %v", tag, err)
				continue
			}
			for _, e := range entries {
				add(e.MosdnsValue())
			}
		}
	}
	sort.Strings(list)
	a.MosdnsProxyDomains = joinLines(list)

	if d.GeoSite != nil {
		entries, err := d.GeoSite.Entries("cn")
		if err != nil {
			a.warn("geosite:cn not available for DNS split: %v", err)
		} else {
			cn := make([]string, 0, len(entries))
			for _, e := range entries {
				cn = append(cn, e.MosdnsValue())
			}
			sort.Strings(cn)
			a.MosdnsCNDomains = joinLines(cn)
		}
	}
	return nil
}

func joinLines(items []string) []byte {
	var buf bytes.Buffer
	for _, it := range items {
		buf.WriteString(it)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// upstreams renders a Mosdns upstream list. Remote (public) resolvers go
// through the local SOCKS inbound so the query itself is proxied; SOCKS has
// no UDP relay here, so plain IPs are forced to TCP.
func upstreams(list []string, viaProxy bool) string {
	var items []string
	for _, raw := range list {
		up, err := rulespec.NormalizeUpstream(raw)
		if err != nil {
			continue
		}
		if viaProxy && rulespec.IsPublicAddr(rulespec.UpstreamHost(up)) {
			items = append(items, fmt.Sprintf(`{ addr: "%s", socks5: "127.0.0.1:%d" }`, forceTCP(up), SocksPort))
		} else {
			items = append(items, fmt.Sprintf(`{ addr: "%s" }`, up))
		}
	}
	if len(items) == 0 {
		if viaProxy {
			return fmt.Sprintf(`[{ addr: "tcp://1.1.1.1", socks5: "127.0.0.1:%d" }, { addr: "tcp://8.8.8.8", socks5: "127.0.0.1:%d" }]`, SocksPort, SocksPort)
		}
		return `[{ addr: "119.29.29.29" }, { addr: "223.5.5.5" }]`
	}
	return "[" + strings.Join(items, ", ") + "]"
}

func forceTCP(up string) string {
	if strings.Contains(up, "://") {
		return strings.Replace(up, "udp://", "tcp://", 1)
	}
	return "tcp://" + up
}

func yamlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
