package main

import (
	"fmt"
	"net"
	"strings"
)

func loadOspfPublishAllowlist() []*net.IPNet {
	var raw string
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='ospf_publish_allowlist'").Scan(&raw); err != nil {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]*net.IPNet, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(p)
		if err != nil || ipNet == nil {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 == nil {
			continue
		}
		out = append(out, ipNet)
	}
	return out
}

func routeAllowedByOspfPublishAllowlist(routeKey string, allowlist []*net.IPNet) bool {
	if len(allowlist) == 0 {
		return true
	}
	normalized, ok := normalizeRouteKey(routeKey)
	if !ok {
		return false
	}
	ipPart := normalized
	if strings.Contains(normalized, "/") {
		ipPart = strings.SplitN(normalized, "/", 2)[0]
	}
	ip := net.ParseIP(ipPart)
	if ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	for _, n := range allowlist {
		if n.Contains(ip4) {
			return true
		}
	}
	return false
}

// validateAdvertisableCIDR enforces an OSPF publish safety policy on top of normalizeRouteKey.
// It rejects default/dirty routes and overly-broad RFC1918 supernets (10/8, 172.16/12, 192.168/16).
func validateAdvertisableCIDR(routeKey string) error {
	normalized, ok := normalizeRouteKey(routeKey)
	if !ok {
		return fmt.Errorf("invalid route")
	}
	_, ipNet, err := net.ParseCIDR(normalized)
	if err != nil || ipNet == nil {
		return fmt.Errorf("invalid CIDR")
	}
	ip4 := ipNet.IP.To4()
	if ip4 == nil {
		return fmt.Errorf("non-ipv4 route")
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 {
		return fmt.Errorf("non-ipv4-mask route")
	}
	// policy hardening: deny RFC1918 supernets from being advertised.
	if ip4[0] == 10 && ones <= 8 {
		return fmt.Errorf("rfc1918 supernet denied: 10.0.0.0/%d", ones)
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 && ones <= 12 {
		return fmt.Errorf("rfc1918 supernet denied: 172.16.0.0/%d", ones)
	}
	if ip4[0] == 192 && ip4[1] == 168 && ones <= 16 {
		return fmt.Errorf("rfc1918 supernet denied: 192.168.0.0/%d", ones)
	}
	return nil
}
