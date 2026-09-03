package remote_deploy

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultRealityPort is the listen port used for a REALITY inbound unless the
// operator explicitly picks another one.
//
// REALITY's camouflage rests on the node being indistinguishable from an
// ordinary TLS reverse proxy for serverName. No real deployment reverse-proxies
// a major site on a random high port, so a port outside the usual TLS range is
// a stronger fingerprint than anything visible at the SNI layer. 443 is the
// only value that keeps that premise intact.
const DefaultRealityPort = 443

// DefaultRealityServerName / DefaultRealityDest are the fallback camouflage
// target. They are a last resort only: every install that accepts the default
// points at the same dest, which is itself a cross-deployment fingerprint.
// Callers should prefer an operator-supplied value.
const (
	DefaultRealityServerName = "www.microsoft.com"
	DefaultRealityDest       = "www.microsoft.com:443"
)

// maxHostnameLen is the maximum length of a DNS name in presentation format.
const maxHostnameLen = 253

// ValidateRealityServerName checks that s is a single, syntactically valid DNS
// hostname usable as a REALITY serverNames entry.
//
// REALITY matches the client's SNI against serverNames verbatim. An empty or
// malformed entry cannot match anything, so every connection — including the
// gateway's own — silently falls through to dest and the node is dead while
// still reporting a successful deploy. Rejecting it up front turns that into a
// deploy-time error instead of a silent outage.
//
// IP literals are rejected: an SNI is a name, and a certificate presented for
// an IP would not validate against it.
func ValidateRealityServerName(s string) error {
	if s == "" {
		return fmt.Errorf("reality serverName must not be empty")
	}
	if strings.TrimSpace(s) != s {
		return fmt.Errorf("reality serverName %q must not contain leading or trailing whitespace", s)
	}
	if len(s) > maxHostnameLen {
		return fmt.Errorf("reality serverName %q exceeds %d characters", s, maxHostnameLen)
	}
	if net.ParseIP(s) != nil {
		return fmt.Errorf("reality serverName %q must be a domain name, not an IP literal", s)
	}
	// A bare label ("localhost") is never a usable camouflage target: no public
	// certificate authority issues for it and no real reverse proxy serves it.
	if !strings.Contains(s, ".") {
		return fmt.Errorf("reality serverName %q must be a fully qualified domain name", s)
	}
	if strings.HasSuffix(s, ".") {
		return fmt.Errorf("reality serverName %q must not be a rooted domain name", s)
	}
	for _, label := range strings.Split(s, ".") {
		if err := validateHostnameLabel(label, s); err != nil {
			return err
		}
	}
	return nil
}

func validateHostnameLabel(label, host string) error {
	if label == "" {
		return fmt.Errorf("hostname %q contains an empty label", host)
	}
	if len(label) > 63 {
		return fmt.Errorf("hostname %q contains a label longer than 63 characters", host)
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("hostname %q contains a label with a leading or trailing hyphen", host)
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '-' {
			return fmt.Errorf("hostname %q contains an invalid character %q", host, string(c))
		}
	}
	return nil
}

// ValidateRealityDest checks that dest is a "host:port" pair REALITY can dial
// as its fallback target. A dest that does not resolve to a real TLS listener
// makes every probe — and every SNI miss — return an obviously broken
// handshake, which is exactly the signal active probing looks for.
//
// The host half may be a domain name or an IP literal (IPv6 in brackets);
// unlike serverName, dest is dialled rather than matched against a certificate.
func ValidateRealityDest(dest string) error {
	if dest == "" {
		return fmt.Errorf("reality dest must not be empty")
	}
	if strings.TrimSpace(dest) != dest {
		return fmt.Errorf("reality dest %q must not contain leading or trailing whitespace", dest)
	}
	host, portStr, err := net.SplitHostPort(dest)
	if err != nil {
		return fmt.Errorf("reality dest %q must be in host:port form: %w", dest, err)
	}
	if host == "" {
		return fmt.Errorf("reality dest %q has an empty host", dest)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("reality dest %q has a non-numeric port %q", dest, portStr)
	}
	if err := ValidatePort(port); err != nil {
		return fmt.Errorf("reality dest %q: %w", dest, err)
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if len(host) > maxHostnameLen {
		return fmt.Errorf("reality dest host %q exceeds %d characters", host, maxHostnameLen)
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if err := validateHostnameLabel(label, host); err != nil {
			return fmt.Errorf("reality dest: %w", err)
		}
	}
	return nil
}

// ValidatePort checks that p is a port a service can listen on or dial.
func ValidatePort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("port %d out of range 1-65535", p)
	}
	return nil
}

// ValidateRealityParams validates a full REALITY inbound parameter set before
// it is baked into an install script and executed as root on a remote host.
func ValidateRealityParams(port int, serverName, dest string) error {
	if err := ValidatePort(port); err != nil {
		return err
	}
	if err := ValidateRealityServerName(serverName); err != nil {
		return err
	}
	return ValidateRealityDest(dest)
}
