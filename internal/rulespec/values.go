package rulespec

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
)

var geoTag = regexp.MustCompile(`^!?[a-z0-9][a-z0-9_@!.-]{0,63}$`)

// NormalizeGeoTag validates a geosite/geoip tag such as "cn", "netflix",
// "category-ads-all", "geolocation-!cn" or "!cn".
func NormalizeGeoTag(v string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(v))
	for _, pfx := range []string{"geosite:", "geoip:"} {
		t = strings.TrimPrefix(t, pfx)
	}
	if !geoTag.MatchString(t) {
		return "", fmt.Errorf("invalid geo tag %q", v)
	}
	return t, nil
}

// NormalizeIPOrCIDR accepts an IPv4/IPv6 address or prefix and returns the
// canonical prefix form (a bare address becomes /32 or /128, a prefix is
// masked to its network address).
func NormalizeIPOrCIDR(v string) (string, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return "", fmt.Errorf("ip value is empty")
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked().String(), nil
	}
	if a, err := netip.ParseAddr(s); err == nil {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()).String(), nil
	}
	return "", fmt.Errorf("invalid ip or cidr %q", v)
}

// IsIPv6Prefix reports whether a normalised prefix string is IPv6.
func IsIPv6Prefix(p string) bool {
	pfx, err := netip.ParsePrefix(p)
	return err == nil && pfx.Addr().Is6()
}

// NormalizeMAC validates and lower-cases a MAC address.
func NormalizeMAC(v string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(v))
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("invalid mac address %q", v)
	}
	return strings.ToLower(hw.String()), nil
}

// NormalizeUpstream validates a DNS upstream for Mosdns: a plain IP, IP:port,
// or a scheme-prefixed address (udp://, tcp://, tls://, https://, quic://).
func NormalizeUpstream(v string) (string, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return "", fmt.Errorf("upstream is empty")
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == ':', c == '/', c == '-', c == '[', c == ']', c == '_':
		default:
			return "", fmt.Errorf("upstream %q contains invalid character %q", v, c)
		}
	}
	return s, nil
}

// UpstreamHost extracts the host part of an upstream for public/private checks.
func UpstreamHost(up string) string {
	s := up
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i >= 0 {
			return s[1:i]
		}
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

// IsPublicAddr reports whether host is an IP outside private/loopback/link-
// local ranges. Hostnames are treated as public (they must be resolved
// somewhere, and that somewhere is not the LAN).
func IsPublicAddr(host string) bool {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return true
	}
	return !(a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified())
}
