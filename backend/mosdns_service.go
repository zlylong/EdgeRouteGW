package main

import "fmt"

func renderMosdnsConfig(local, remote string, lazy bool, mode string) string {
	lazyCache := ""
	lazyExec := ""
	if lazy {
		lazyCache = "  - tag: lazy_cache\n    type: cache\n    args: { size: 10240, lazy_cache_ttl: 86400 }\n"
		lazyExec = "      - exec: $lazy_cache\n      - matches: [ has_resp ]\n        exec: return\n"
	}

	proxyDomainExec := "exec: $forward_remote"
	fakeIPForwardPlugin := ""
	if mode == "B" {
		proxyDomainExec = "exec: $forward_fakeip"
		fakeIPForwardPlugin = "  - tag: forward_fakeip\n    type: forward\n    args: { upstreams: [{ addr: \"127.0.0.1:5353\" }] }\n"
	}

	configStr := `log:
  level: info
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
`

	return fmt.Sprintf(configStr,
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
