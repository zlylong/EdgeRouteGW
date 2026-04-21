package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

var geoQueryLookupIP = func(host string) ([]string, error) {
	resolver := net.Resolver{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(ips))
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip.IP == nil || strings.Contains(ip.IP.String(), ":") {
			continue
		}
		s := ip.IP.String()
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

type geoSiteDomainEntry struct {
	Type  string
	Value string
}

func normalizeGeoQueryInput(input string) string {
	return strings.TrimSpace(strings.ToLower(input))
}

func skipProtoField(data []byte, idx int, field byte) int {
	wireType := field & 0x07
	switch wireType {
	case 0:
		_, idx = parseVarint(data, idx)
	case 2:
		l, newIdx := parseVarint(data, idx)
		idx = newIdx + l
	}
	if idx < 0 {
		return 0
	}
	if idx > len(data) {
		return len(data)
	}
	return idx
}

func parseGeoRuleInput(input string) (kind string, tag string, ok bool) {
	normalized := normalizeGeoQueryInput(input)
	switch {
	case strings.HasPrefix(normalized, "geoip:"):
		tag = strings.TrimSpace(strings.TrimPrefix(normalized, "geoip:"))
		return "geoip", tag, tag != ""
	case strings.HasPrefix(normalized, "geosite:"):
		tag = strings.TrimSpace(strings.TrimPrefix(normalized, "geosite:"))
		return "geosite", tag, tag != ""
	default:
		return "", "", false
	}
}

func queryGeoIPTagsByIP(filename, input string) []string {
	ip := net.ParseIP(strings.TrimSpace(input))
	if ip == nil {
		return nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}

	matches := make([]string, 0)
	idx := 0
	for idx < len(data) {
		if data[idx] != 0x0A {
			idx++
			continue
		}
		idx++
		msgLen, newIdx := parseVarint(data, idx)
		idx = newIdx
		endIdx := idx + msgLen
		if endIdx > len(data) {
			endIdx = len(data)
		}

		tag := ""
		matched := false
		for idx < endIdx {
			field := data[idx]
			idx++
			switch field {
			case 0x0A:
				strLen, nIdx := parseVarint(data, idx)
				idx = nIdx
				if idx+strLen > endIdx {
					idx = endIdx
					break
				}
				tag = strings.ToLower(string(data[idx : idx+strLen]))
				idx += strLen
			case 0x12:
				cidrLen, nIdx := parseVarint(data, idx)
				idx = nIdx
				cidrEnd := idx + cidrLen
				if cidrEnd > endIdx {
					cidrEnd = endIdx
				}
				var ipBytes []byte
				prefix := 0
				for idx < cidrEnd {
					f := data[idx]
					idx++
					switch f {
					case 0x0A:
						ipLen, nn := parseVarint(data, idx)
						idx = nn
						if idx+ipLen > cidrEnd {
							idx = cidrEnd
							break
						}
						ipBytes = append([]byte(nil), data[idx:idx+ipLen]...)
						idx += ipLen
					case 0x10:
						prefix, idx = parseVarint(data, idx)
					default:
						idx = skipProtoField(data, idx, f)
					}
				}
				if len(ipBytes) > 0 {
					if _, cidr, err := net.ParseCIDR(fmt.Sprintf("%s/%d", net.IP(ipBytes).String(), prefix)); err == nil && cidr.Contains(ip) {
						matched = true
					}
				}
			default:
				idx = skipProtoField(data, idx, field)
			}
		}
		if matched && tag != "" {
			matches = append(matches, tag)
		}
		idx = endIdx
	}
	sort.Strings(matches)
	return matches
}

func matchGeoSiteDomain(domain string, entry geoSiteDomainEntry) bool {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	value := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(entry.Value)), ".")
	switch entry.Type {
	case "full":
		return domain == value
	case "domain":
		return domain == value || strings.HasSuffix(domain, "."+value)
	case "keyword":
		return strings.Contains(domain, value)
	case "regex":
		matched, err := regexp.MatchString(value, domain)
		return err == nil && matched
	default:
		return false
	}
}

func scanGeoSiteEntries(filename string, fn func(tag string, entries []geoSiteDomainEntry)) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return
	}
	idx := 0
	for idx < len(data) {
		if data[idx] != 0x0A {
			idx++
			continue
		}
		idx++
		msgLen, newIdx := parseVarint(data, idx)
		idx = newIdx
		endIdx := idx + msgLen
		if endIdx > len(data) {
			endIdx = len(data)
		}

		tag := ""
		entries := make([]geoSiteDomainEntry, 0)
		for idx < endIdx {
			field := data[idx]
			idx++
			switch field {
			case 0x0A:
				strLen, nIdx := parseVarint(data, idx)
				idx = nIdx
				if idx+strLen > endIdx {
					idx = endIdx
					break
				}
				tag = strings.ToLower(string(data[idx : idx+strLen]))
				idx += strLen
			case 0x12:
				domainLen, nIdx := parseVarint(data, idx)
				idx = nIdx
				domainEnd := idx + domainLen
				if domainEnd > endIdx {
					domainEnd = endIdx
				}
				domainType := 0
				value := ""
				for idx < domainEnd {
					f := data[idx]
					idx++
					switch f {
					case 0x08:
						domainType, idx = parseVarint(data, idx)
					case 0x12:
						strLen, nn := parseVarint(data, idx)
						idx = nn
						if idx+strLen > domainEnd {
							idx = domainEnd
							break
						}
						value = string(data[idx : idx+strLen])
						idx += strLen
					default:
						idx = skipProtoField(data, idx, f)
					}
				}
				if strings.TrimSpace(value) != "" {
					entryType := "keyword"
					switch domainType {
					case 1:
						entryType = "regex"
					case 2:
						entryType = "domain"
					case 3:
						entryType = "full"
					}
					entries = append(entries, geoSiteDomainEntry{Type: entryType, Value: value})
				}
			default:
				idx = skipProtoField(data, idx, field)
			}
		}
		if tag != "" {
			fn(tag, entries)
		}
		idx = endIdx
	}
}

func queryGeoSiteTagsByDomain(filename, input string) []string {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(input)), ".")
	if domain == "" {
		return nil
	}
	matches := make([]string, 0)
	scanGeoSiteEntries(filename, func(tag string, entries []geoSiteDomainEntry) {
		for _, entry := range entries {
			if matchGeoSiteDomain(domain, entry) {
				matches = append(matches, tag)
				return
			}
		}
	})
	sort.Strings(matches)
	return matches
}

func extractGeoSiteValues(filename, targetTag string) []string {
	targetTag = strings.ToLower(strings.TrimSpace(targetTag))
	values := make([]string, 0)
	scanGeoSiteEntries(filename, func(tag string, entries []geoSiteDomainEntry) {
		if tag != targetTag {
			return
		}
		for _, entry := range entries {
			values = append(values, fmt.Sprintf("%s:%s", entry.Type, entry.Value))
		}
	})
	return values
}

func hasGeoSiteTag(filename, targetTag string) bool {
	targetTag = strings.ToLower(strings.TrimSpace(targetTag))
	found := false
	scanGeoSiteEntries(filename, func(tag string, _ []geoSiteDomainEntry) {
		if tag == targetTag {
			found = true
		}
	})
	return found
}

func hasGeoIPTag(filename, targetTag string) bool {
	targetTag = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(targetTag, "!")))
	if targetTag == "" {
		return false
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return false
	}
	idx := 0
	for idx < len(data) {
		if data[idx] != 0x0A {
			idx++
			continue
		}
		idx++
		msgLen, newIdx := parseVarint(data, idx)
		idx = newIdx
		endIdx := idx + msgLen
		if endIdx > len(data) {
			endIdx = len(data)
		}
		for idx < endIdx {
			field := data[idx]
			idx++
			if field == 0x0A {
				strLen, nIdx := parseVarint(data, idx)
				idx = nIdx
				if idx+strLen > endIdx {
					idx = endIdx
					break
				}
				if strings.ToUpper(string(data[idx:idx+strLen])) == targetTag {
					return true
				}
				idx += strLen
				continue
			}
			idx = skipProtoField(data, idx, field)
		}
		idx = endIdx
	}
	return false
}
