import re

with open('/root/proxygw/backend/ospf_dns_cache.go', 'r') as f:
    content = f.read()

# 1. Add "os/exec" and "bytes" and "strings" to imports if not already there, and keep others
# (Skipping manual import management for now as it's complex with regex, let's focus on the logic)

# 2. Rewrite lookupIPv4WithDNSServer to use dig COMMAND directly
new_lookup = """func lookupIPv4WithDNSServer(domain string, server string, _ bool) ([]string, error) {
	serverAddr, ok := normalizeDNSServerAddr(server)
	if !ok {
		return nil, fmt.Errorf("invalid dns server %q", server)
	}

	// Use dig command to avoid Go's internal /etc/resolv.conf fallback and logic.
	// This ensures what we see in 'dig @127.0.0.1' is exactly what the engine gets.
	ctx, cancel := context.WithTimeout(context.Background(), domainResolveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dig", "+short", "+timeout=2", "+tries=1", "@"+serverAddr, domain)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dig execution failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\\n")
	var ips []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if net.ParseIP(line) != nil && strings.Contains(line, ".") {
			ips = append(ips, line)
		}
	}

	ips = normalizeIPList(ips)
	if len(ips) == 0 {
		return nil, fmt.Errorf("no A records found via dig")
	}
	return ips, nil
}"""

# Use a very specific pattern to replace the existing function
# The pattern matches the function from its signature to the final return
pattern = re.compile(r'func lookupIPv4WithDNSServer\(domain string, server string, _ bool\) \(\[\]string, error\) \{.*?return ips, nil\n\}', re.DOTALL)
content = pattern.sub(new_lookup, content)

with open('/root/proxygw/backend/ospf_dns_cache.go', 'w') as f:
    f.write(content)
