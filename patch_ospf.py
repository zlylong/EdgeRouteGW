import re

with open('/root/proxygw/backend/ospf_dns_cache.go', 'r') as f:
    content = f.read()

new_func = """func getResolverDNSServers(resolverGroup string) []string {
	// CRITICAL FIX: To prevent DNS leaks and GFW poisoning (e.g. 119.29.x.x), 
	// backend OSPF resolution MUST ALWAYS use the local Mosdns instance (127.0.0.1).
	// Mosdns handles the SOCKS5 proxying to the actual dns_remote.
	return []string{"127.0.0.1"}
}"""

# Find the function and replace it
pattern = re.compile(r'func getResolverDNSServers.*?return servers\n}', re.DOTALL)
content = pattern.sub(new_func, content)

with open('/root/proxygw/backend/ospf_dns_cache.go', 'w') as f:
    f.write(content)
