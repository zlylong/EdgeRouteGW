package main

import (
	"container/list"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
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

type geoIPBucketRule struct {
	network uint32
	mask    uint32
	prefix  uint8
	tag     string
}

type geoIPMatcher struct {
	version string
	buckets [256][]geoIPBucketRule
	tags    map[string]struct{}
}

const (
	geoIPTagLookupCacheTTL        = 10 * time.Minute
	geoIPTagLookupCacheMaxEntries = 200000
)

type geoIPTagCacheEntry struct {
	key       geoIPCacheKey
	tags      []string
	expiresAt time.Time
}

type geoIPLookupCall struct {
	done chan struct{}
	tags []string
}

type geoIPCacheKey struct {
	filename string
	version  string
	ip       uint32
}

var (
	geoIPMatcherMu         sync.RWMutex
	geoIPMatcherCache      = map[string]*geoIPMatcher{}
	geoDataVersionCacheMu  sync.Mutex
	geoDataVersionCached   string
	geoDataVersionCachedAt time.Time

	geoIPTagCacheMu   sync.Mutex
	geoIPTagCacheList = list.New()
	geoIPTagCacheMap  = map[geoIPCacheKey]*list.Element{}

	geoIPLookupCallMu sync.Mutex
	geoIPLookupCalls  = map[geoIPCacheKey]*geoIPLookupCall{}
)

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func geoIPTagCacheGet(key geoIPCacheKey) ([]string, bool) {
	geoIPTagCacheMu.Lock()
	defer geoIPTagCacheMu.Unlock()
	ele := geoIPTagCacheMap[key]
	if ele == nil {
		return nil, false
	}
	entry, _ := ele.Value.(*geoIPTagCacheEntry)
	if entry == nil || time.Now().After(entry.expiresAt) {
		geoIPTagCacheList.Remove(ele)
		delete(geoIPTagCacheMap, key)
		return nil, false
	}
	geoIPTagCacheList.MoveToFront(ele)
	return cloneStringSlice(entry.tags), true
}

func geoIPTagCacheSet(key geoIPCacheKey, tags []string) {
	geoIPTagCacheMu.Lock()
	defer geoIPTagCacheMu.Unlock()
	if ele := geoIPTagCacheMap[key]; ele != nil {
		if entry, _ := ele.Value.(*geoIPTagCacheEntry); entry != nil {
			entry.tags = cloneStringSlice(tags)
			entry.expiresAt = time.Now().Add(geoIPTagLookupCacheTTL)
			geoIPTagCacheList.MoveToFront(ele)
			return
		}
	}
	entry := &geoIPTagCacheEntry{key: key, tags: cloneStringSlice(tags), expiresAt: time.Now().Add(geoIPTagLookupCacheTTL)}
	ele := geoIPTagCacheList.PushFront(entry)
	geoIPTagCacheMap[key] = ele
	for len(geoIPTagCacheMap) > geoIPTagLookupCacheMaxEntries {
		last := geoIPTagCacheList.Back()
		if last == nil {
			break
		}
		lastEntry, _ := last.Value.(*geoIPTagCacheEntry)
		geoIPTagCacheList.Remove(last)
		if lastEntry != nil {
			delete(geoIPTagCacheMap, lastEntry.key)
		}
	}
}

func fastGeoDataVersion() string {
	geoDataVersionCacheMu.Lock()
	defer geoDataVersionCacheMu.Unlock()
	if geoDataVersionCached != "" && time.Since(geoDataVersionCachedAt) < 2*time.Second {
		return geoDataVersionCached
	}
	geoDataVersionCached = getGeoDataVersion()
	geoDataVersionCachedAt = time.Now()
	return geoDataVersionCached
}

func ipv4ToUint32(ip net.IP) (uint32, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	return (uint32(ip4[0]) << 24) | (uint32(ip4[1]) << 16) | (uint32(ip4[2]) << 8) | uint32(ip4[3]), true
}

func loadGeoIPMatcher(filename string) *geoIPMatcher {
	ver := fastGeoDataVersion()
	geoIPMatcherMu.RLock()
	cached := geoIPMatcherCache[filename]
	geoIPMatcherMu.RUnlock()
	if cached != nil && cached.version == ver {
		return cached
	}

	geoIPMatcherMu.Lock()
	defer geoIPMatcherMu.Unlock()
	cached = geoIPMatcherCache[filename]
	if cached != nil && cached.version == ver {
		return cached
	}

	matcher, err := buildGeoIPMatcher(filename, ver)
	if err != nil {
		log.Printf("[WARN] build geoip matcher failed: %v", err)
		return nil
	}
	geoIPMatcherCache[filename] = matcher
	return matcher
}

func buildGeoIPMatcher(filename string, version string) (*geoIPMatcher, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	matcher := &geoIPMatcher{version: version, tags: make(map[string]struct{})}
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
				matcher.tags[tag] = struct{}{}
				idx += strLen
			case 0x12:
				cidrLen, nIdx := parseVarint(data, idx)
				idx = nIdx
				cidrEnd := idx + cidrLen
				if cidrEnd > endIdx {
					cidrEnd = endIdx
				}
				if tag == "" {
					idx = cidrEnd
					continue
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
						ipBytes = append(ipBytes[:0], data[idx:idx+ipLen]...)
						idx += ipLen
					case 0x10:
						prefix, idx = parseVarint(data, idx)
					default:
						idx = skipProtoField(data, idx, f)
					}
				}
				if len(ipBytes) == 0 {
					continue
				}
				ipValue, ok := ipv4ToUint32(net.IP(ipBytes))
				if !ok {
					continue
				}
				if prefix < 0 {
					prefix = 0
				}
				if prefix > 32 {
					prefix = 32
				}
				var mask uint32
				if prefix == 0 {
					mask = 0
				} else {
					mask = ^uint32(0) << uint(32-prefix)
				}
				network := ipValue & mask
				end := network | ^mask
				startFirst := int((network >> 24) & 0xFF)
				endFirst := int((end >> 24) & 0xFF)
				rule := geoIPBucketRule{network: network, mask: mask, prefix: uint8(prefix), tag: tag}
				for first := startFirst; first <= endFirst; first++ {
					matcher.buckets[first] = append(matcher.buckets[first], rule)
				}
			default:
				idx = skipProtoField(data, idx, field)
			}
		}
		idx = endIdx
	}
	for i := range matcher.buckets {
		bucket := matcher.buckets[i]
		if len(bucket) < 2 {
			continue
		}
		sort.Slice(bucket, func(a, b int) bool {
			if bucket[a].prefix != bucket[b].prefix {
				return bucket[a].prefix > bucket[b].prefix
			}
			if bucket[a].network != bucket[b].network {
				return bucket[a].network < bucket[b].network
			}
			return bucket[a].tag < bucket[b].tag
		})
		matcher.buckets[i] = bucket
	}
	return matcher, nil
}

func queryGeoIPTagsByIP(filename, input string) []string {
	ip := net.ParseIP(strings.TrimSpace(input))
	ipValue, ok := ipv4ToUint32(ip)
	if !ok {
		return nil
	}
	matcher := loadGeoIPMatcher(filename)
	if matcher == nil {
		return nil
	}

	cacheKey := geoIPCacheKey{filename: filename, version: matcher.version, ip: ipValue}
	if cached, ok := geoIPTagCacheGet(cacheKey); ok {
		return cached
	}

	geoIPLookupCallMu.Lock()
	if call := geoIPLookupCalls[cacheKey]; call != nil {
		geoIPLookupCallMu.Unlock()
		<-call.done
		return cloneStringSlice(call.tags)
	}
	call := &geoIPLookupCall{done: make(chan struct{})}
	geoIPLookupCalls[cacheKey] = call
	geoIPLookupCallMu.Unlock()

	bucket := matcher.buckets[int((ipValue>>24)&0xFF)]
	var matches []string
	if len(bucket) > 0 {
		matchedTags := make(map[string]struct{}, 4)
		for _, rule := range bucket {
			if _, done := matchedTags[rule.tag]; done {
				continue
			}
			if (ipValue & rule.mask) == rule.network {
				matchedTags[rule.tag] = struct{}{}
			}
		}
		if len(matchedTags) > 0 {
			matches = make([]string, 0, len(matchedTags))
			for tag := range matchedTags {
				matches = append(matches, tag)
			}
			sort.Strings(matches)
		}
	}

	geoIPTagCacheSet(cacheKey, matches)

	geoIPLookupCallMu.Lock()
	call.tags = cloneStringSlice(matches)
	close(call.done)
	delete(geoIPLookupCalls, cacheKey)
	geoIPLookupCallMu.Unlock()
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
	_ = scanGeoSiteEntriesE(filename, fn)
}

func scanGeoSiteEntriesE(filename string, fn func(tag string, entries []geoSiteDomainEntry)) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
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
	return nil
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

func extractGeoSiteResolvableDomains(filename, targetTag string) ([]string, int, error) {
	targetTag = strings.ToLower(strings.TrimSpace(targetTag))
	if targetTag == "" {
		return nil, 0, nil
	}
	seen := make(map[string]struct{})
	domains := make([]string, 0)
	skipped := 0
	if err := scanGeoSiteEntriesE(filename, func(tag string, entries []geoSiteDomainEntry) {
		if tag != targetTag {
			return
		}
		for _, entry := range entries {
			switch entry.Type {
			case "domain", "full":
				domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(entry.Value)), ".")
				if domain == "" {
					continue
				}
				if _, ok := seen[domain]; ok {
					continue
				}
				seen[domain] = struct{}{}
				domains = append(domains, domain)
			default:
				skipped++
			}
		}
	}); err != nil {
		return nil, 0, err
	}
	sort.Strings(domains)
	return domains, skipped, nil
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
	targetTag = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(targetTag, "!")))
	if targetTag == "" {
		return false
	}
	matcher := loadGeoIPMatcher(filename)
	if matcher == nil {
		return false
	}
	_, ok := matcher.tags[targetTag]
	return ok
}
