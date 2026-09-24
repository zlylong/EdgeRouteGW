package main

import (
	"fmt"
	"strings"
)

var mosdnsLogLevels = map[string]struct{}{"debug": {}, "info": {}, "warn": {}, "error": {}}

func isValidMosdnsLogLevel(level string) bool {
	_, ok := mosdnsLogLevels[strings.ToLower(strings.TrimSpace(level))]
	return ok
}

// isValidDNSMode accepts the resolution modes the UI offers. Only "smart"
// exists today; the whitelist keeps arbitrary strings out of the settings row.
func isValidDNSMode(mode string) bool {
	return strings.ToLower(strings.TrimSpace(mode)) == "smart"
}

func renderMosdnsConfig(local, remote string, lazy bool, mode string, logLevel string, cacheSize int, lazyTTL int) string {
	// Defensive: the controller validates too, but a value already stored in
	// settings must not be able to break out of the YAML scalar either.
	if !isValidMosdnsLogLevel(logLevel) {
		logLevel = "info"
	}
	lazyCache := ""
	lazyExec := ""
	if lazy {
		if cacheSize <= 0 {
			cacheSize = 10240
		}
		if lazyTTL <= 0 {
			lazyTTL = 86400
		}
		lazyCache = fmt.Sprintf("  - tag: lazy_cache\n    type: cache\n    args: { size: %d, lazy_cache_ttl: %d }\n", cacheSize, lazyTTL)
		lazyExec = "      - exec: $lazy_cache\n      - matches: [ has_resp ]\n        exec: return\n"
	}

	if logLevel == "" {
		logLevel = "info"
	}

	proxyDomainExec := "exec: $forward_remote"
	fakeIPForwardPlugin := ""
	if mode == "B" {
		proxyDomainExec = "exec: $forward_fakeip"
		fakeIPForwardPlugin = "  - tag: forward_fakeip\n    type: forward\n    args: { upstreams: [{ addr: \"127.0.0.1:5353\" }] }\n"
	}

	configStr := `log:
  level: %s
  file: %s
plugins:
  - tag: proxy_domain
    type: domain_set
    args:
      files:
        - %s
  - tag: geosite_cn
    type: domain_set
    args:
      files:
        - %s
%s  - tag: forward_local
    type: forward
    args: { upstreams: %s }
  - tag: forward_remote
    type: forward
    args: { upstreams: %s }
%s
  - tag: main_sequence
    type: sequence
    args:
%s      - matches: [ qname $proxy_domain ]
        %s
      - matches: [ has_resp ]
        exec: return
      - matches: [ qname $geosite_cn ]
        exec: $forward_local
      - matches: [ has_resp ]
        exec: return
      - exec: $forward_remote
  - tag: udp_server
    type: udp_server
    args:
      entry: main_sequence
      listen: 0.0.0.0:53
  - tag: tcp_server
    type: tcp_server
    args:
      entry: main_sequence
      listen: 0.0.0.0:53
`

	return fmt.Sprintf(configStr,
		logLevel,
		getPath("core", "mosdns", "mosdns.log"),
		getPath("core", "mosdns", "proxy_domains.txt"),
		getPath("core", "mosdns", "geosite_cn.txt"),
		lazyCache,
		formatUpstreams(local, false),
		formatUpstreams(remote, true),
		fakeIPForwardPlugin,
		lazyExec,
		proxyDomainExec,
	)
}
