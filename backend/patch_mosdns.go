package main

import (
	"net"
	"strings"
)

func isPublicDNSTarget(addr string) bool {
	host := addr
	if strings.Contains(addr, "://") {
		parts := strings.SplitN(addr, "://", 2)
		host = parts[1]
	}
	if idx := strings.Index(host, "/"); idx != -1 {
		host = host[:idx]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// If it's a domain name (e.g. dns.google), treat as public
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	return true
}

func forceMosdnsTCPAddr(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "tcp://" + addr
}
