import re

with open('/root/proxygw/backend/ospf_dns_cache.go', 'r') as f:
    content = f.read()

# Replace resolveDomainIPv4WithTTL to only use 127.0.0.1 via dig, NO FALLBACK
new_resolve_ttl = r"""var resolveDomainIPv4WithTTL = func(domain string) ([]string, int, error) {
	// 100% force use local Mosdns via dig. No more OS 'host' command or direct DNS.
	// This ensures we always get clean results from our proxied Mosdns.
	ips, err := lookupIPv4WithDNSServer(domain, "127.0.0.1", false)
	if err != nil {
		return nil, 0, err
	}
	return ips, minDomainCacheTTLSeconds, nil
}"""

pattern = re.compile(r'var resolveDomainIPv4WithTTL = func\(domain string\) \(\[\]string, int, error\) \{.*?return ips, minDomainCacheTTLSeconds, nil\n\}', re.DOTALL)
content = pattern.sub(new_resolve_ttl, content)

with open('/root/proxygw/backend/ospf_dns_cache.go', 'w') as f:
    f.write(content)
