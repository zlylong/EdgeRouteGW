import re

with open('/root/proxygw/backend/ospf_dns_cache.go', 'r') as f:
    content = f.read()

# 1. Clean up imports - remove proxy if present (to keep it simple)
content = content.replace('"golang.org/x/net/proxy"', '')

# 2. Force getResolverDNSServers to 127.0.0.1 only
new_get_servers = """func getResolverDNSServers(resolverGroup string) []string {
	// 100% delegate to local Mosdns. No more direct external queries from OSPF engine.
	return []string{"127.0.0.1"}
}"""
content = re.sub(r'func getResolverDNSServers.*?return servers\n}', new_get_servers, content, flags=re.DOTALL)

# 3. Simplify lookupIPv4WithDNSServer - REMOVE SOCKS5 and useProxy junk
new_lookup = """func lookupIPv4WithDNSServer(domain string, server string, _ bool) ([]string, error) {
	serverAddr, ok := normalizeDNSServerAddr(server)
	if !ok {
		return nil, fmt.Errorf("invalid dns server %q", server)
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: domainResolveTimeout}
			return d.DialContext(ctx, "udp", net.JoinHostPort(serverAddr, "53"))
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainResolveTimeout)
	defer cancel()
	addrs, err := resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		return nil, err
	}
	ips := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IP == nil || addr.IP.To4() == nil {
			continue
		}
		ips = append(ips, addr.IP.String())
	}
	ips = normalizeIPList(ips)
	if len(ips) == 0 {
		return nil, fmt.Errorf("no A records")
	}
	return ips, nil
}"""
content = re.sub(r'func lookupIPv4WithDNSServer\(domain string, server string, useProxy bool\) \(\[\]string, error\) \{.*?\n\treturn ips, nil\n\}', new_lookup, content, flags=re.DOTALL)

# 4. Simplify resolveDomainIPv4WithTTLViaServers - REMOVE host fallback and useProxy logic
new_resolve_via = """var resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string, isRemote bool) ([]string, int, error) {
	if len(dnsServers) == 0 {
		return resolveDomainIPv4WithTTL(domain)
	}
	var firstErr error
	for _, server := range dnsServers {
		// No more OS 'host' command for OSPF expansion. 
		// We trust our resolver to query 127.0.0.1 (Mosdns).
		ips, lookupErr := lookupIPv4WithDNSServer(domain, server, isRemote)
		if lookupErr == nil {
			return ips, minDomainCacheTTLSeconds, nil
		}
		if firstErr == nil {
			firstErr = lookupErr
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("all dns servers failed")
	}
	return nil, 0, firstErr
}"""
content = re.sub(r'var resolveDomainIPv4WithTTLViaServers = func\(domain string, dnsServers \[\]string, isRemote bool\) \(\[\]string, int, error\) \{.*?return nil, 0, firstErr\n\}', new_resolve_via, content, flags=re.DOTALL)

with open('/root/proxygw/backend/ospf_dns_cache.go', 'w') as f:
    f.write(content)
