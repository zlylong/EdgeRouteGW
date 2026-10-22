package geo

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Version fingerprints the geoip file (size and mtime), like Site.Version.
func (g *IP) Version() (string, error) {
	st, err := os.Stat(g.path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%d", st.Size(), st.ModTime().UnixNano()), nil
}

// IPIndex answers "which geoip tags contain this address". It is built
// lazily from the reader and rebuilt when the file changes. Lists are
// sorted per tag; lookup is a binary search plus a short backward scan for
// the rare nested prefixes.
type IPIndex struct {
	src  *IP
	mu   sync.Mutex
	ver  string
	tags map[string][]netip.Prefix
}

// NewIPIndex wraps a reader.
func NewIPIndex(src *IP) *IPIndex { return &IPIndex{src: src} }

func (x *IPIndex) ensure() error {
	ver, err := x.src.Version()
	if err != nil {
		return err
	}
	if x.tags != nil && ver == x.ver {
		return nil
	}
	tags, err := x.src.Tags()
	if err != nil {
		return err
	}
	idx := make(map[string][]netip.Prefix, len(tags))
	for _, t := range tags {
		ps, err := x.src.CIDRs(t)
		if err != nil {
			continue
		}
		ps = append([]netip.Prefix(nil), ps...)
		sort.Slice(ps, func(i, j int) bool { return ps[i].Addr().Less(ps[j].Addr()) })
		idx[t] = ps
	}
	x.tags, x.ver = idx, ver
	return nil
}

// Lookup returns the sorted tags whose lists contain addr.
func (x *IPIndex) Lookup(addr netip.Addr) ([]string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.ensure(); err != nil {
		return nil, err
	}
	addr = addr.Unmap()
	var out []string
	for tag, ps := range x.tags {
		i := sort.Search(len(ps), func(i int) bool { return addr.Less(ps[i].Addr()) }) - 1
		for j := i; j >= 0 && j > i-16; j-- {
			if ps[j].Contains(addr) {
				out = append(out, tag)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// SiteIndex answers "which geosite tags match this domain". Exact and
// root-domain entries live in two slices sorted by domain, each entry
// carrying its tag as a 16-bit index; a lookup is a binary search. This is
// several times smaller than maps of slices for the ~600k entries of a
// full geosite.dat, which matters on a 2 GB gateway.
type SiteIndex struct {
	src     *Site
	mu      sync.Mutex
	ver     string
	tags    []string
	full    []siteRef
	root    []siteRef
	keyword []siteKW
	regex   []siteRX
}

type siteRef struct {
	key string
	tag uint16
}
type siteKW struct {
	tag   uint16
	value string
}
type siteRX struct {
	tag uint16
	re  *regexp.Regexp
}

// NewSiteIndex wraps a reader.
func NewSiteIndex(src *Site) *SiteIndex { return &SiteIndex{src: src} }

func (x *SiteIndex) ensure() error {
	ver, err := x.src.Version()
	if err != nil {
		return err
	}
	if x.tags != nil && ver == x.ver {
		return nil
	}
	tags, err := x.src.Tags()
	if err != nil {
		return err
	}
	if len(tags) > 65535 {
		tags = tags[:65535]
	}
	var full, root []siteRef
	var kws []siteKW
	var rxs []siteRX
	for i, t := range tags {
		entries, err := x.src.EntriesUncached(t)
		if err != nil {
			continue
		}
		ti := uint16(i)
		for _, e := range entries {
			v := strings.ToLower(e.Value)
			switch e.Kind {
			case KindFull:
				full = append(full, siteRef{v, ti})
			case KindRoot:
				root = append(root, siteRef{v, ti})
			case KindPlain:
				kws = append(kws, siteKW{ti, v})
			case KindRegex:
				if re, err := regexp.Compile(e.Value); err == nil {
					rxs = append(rxs, siteRX{ti, re})
				}
			}
		}
	}
	sort.Slice(full, func(i, j int) bool { return full[i].key < full[j].key })
	sort.Slice(root, func(i, j int) bool { return root[i].key < root[j].key })
	x.tags, x.full, x.root, x.keyword, x.regex, x.ver = tags, full, root, kws, rxs, ver
	runtime.GC()
	return nil
}

// refsFor appends the tag indexes whose key equals k.
func refsFor(list []siteRef, k string, set map[uint16]bool) {
	i := sort.Search(len(list), func(i int) bool { return list[i].key >= k })
	for ; i < len(list) && list[i].key == k; i++ {
		set[list[i].tag] = true
	}
}

// Lookup returns the sorted tags matching domain.
func (x *SiteIndex) Lookup(domain string) ([]string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.ensure(); err != nil {
		return nil, err
	}
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	set := map[uint16]bool{}
	refsFor(x.full, domain, set)
	for d := domain; d != ""; {
		refsFor(x.root, d, set)
		i := strings.IndexByte(d, '.')
		if i < 0 {
			break
		}
		d = d[i+1:]
	}
	for _, kw := range x.keyword {
		if strings.Contains(domain, kw.value) {
			set[kw.tag] = true
		}
	}
	for _, rx := range x.regex {
		if rx.re.MatchString(domain) {
			set[rx.tag] = true
		}
	}
	out := make([]string, 0, len(set))
	for ti := range set {
		out = append(out, x.tags[ti])
	}
	sort.Strings(out)
	return out, nil
}

// Labels returns "geoip:x"/"geosite:x" labels for a target, which may be
// "host:port"; used by the connection tracker and the lookup endpoint.
func Labels(site *SiteIndex, ip *IPIndex, target string) []string {
	host := target
	if h, _, err := SplitHostPort(target); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	var out []string
	if addr, err := netip.ParseAddr(host); err == nil {
		if ip != nil {
			if tags, err := ip.Lookup(addr); err == nil {
				for _, t := range tags {
					out = append(out, "geoip:"+t)
				}
			}
		}
		return out
	}
	if site != nil && strings.Contains(host, ".") {
		if tags, err := site.Lookup(host); err == nil {
			for _, t := range tags {
				out = append(out, "geosite:"+t)
			}
		}
	}
	return out
}

// SplitHostPort splits "host:port" leniently (bare IPv6 without port is
// reported as having none).
func SplitHostPort(s string) (string, string, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 || strings.Count(s, ":") > 1 && !strings.HasPrefix(s, "[") {
		return "", "", errNoPort
	}
	return s[:i], s[i+1:], nil
}

var errNoPort = errors.New("no port")
