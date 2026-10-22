// Package geo reads V2Ray/Xray geosite.dat and geoip.dat files. The files are
// protobuf (GeoSiteList / GeoIPList); this package decodes them with the
// low-level wire reader so it needs no generated code and tolerates fields it
// does not know about.
//
// Loading is lazy and cached per file identity (size + mtime): the top-level
// list is split into per-tag byte slices once, and a tag is decoded only when
// first asked for.
package geo

import (
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// DomainKind mirrors xray.app.router.Domain.Type.
type DomainKind int

const (
	KindPlain   DomainKind = 0 // substring ("keyword")
	KindRegex   DomainKind = 1
	KindRoot    DomainKind = 2 // domain and subdomains
	KindFull    DomainKind = 3
	kindUnknown DomainKind = -1
)

// Entry is one geosite domain record.
type Entry struct {
	Kind  DomainKind
	Value string
	Attrs []string
}

// MosdnsValue renders the entry in mosdns domain_set syntax.
func (e Entry) MosdnsValue() string {
	switch e.Kind {
	case KindPlain:
		return "keyword:" + e.Value
	case KindRegex:
		return "regexp:" + e.Value
	case KindRoot:
		return "domain:" + e.Value
	default:
		return "full:" + e.Value
	}
}

// XrayValue renders the entry in Xray routing syntax.
func (e Entry) XrayValue() string {
	switch e.Kind {
	case KindPlain:
		return "keyword:" + e.Value
	case KindRegex:
		return "regexp:" + e.Value
	case KindRoot:
		return "domain:" + e.Value
	default:
		return "full:" + e.Value
	}
}

// Resolvable reports whether the entry names a concrete domain that DNS can
// resolve (root/full), as opposed to a keyword or regex.
func (e Entry) Resolvable() bool { return e.Kind == KindRoot || e.Kind == KindFull }

type fileID struct {
	size    int64
	modTime time.Time
}

// Site is a lazily loaded geosite.dat.
type Site struct {
	path string

	mu      sync.Mutex
	id      fileID
	raw     map[string][]byte // tag -> encoded GeoSite message
	decoded map[string][]Entry
}

// OpenSite returns a reader for path. The file is read on first use.
func OpenSite(path string) *Site { return &Site{path: path} }

// Path returns the file this reader watches.
func (s *Site) Path() string { return s.path }

// Tags lists every tag in the file, sorted.
func (s *Site) Tags() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(s.raw))
	for t := range s.raw {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// HasTag reports whether tag exists.
func (s *Site) HasTag(tag string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return false, err
	}
	_, ok := s.raw[strings.ToLower(tag)]
	return ok, nil
}

// Entries returns the decoded records of tag, or ErrTagNotFound.
func (s *Site) Entries(tag string) ([]Entry, error) {
	tag = strings.ToLower(tag)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	if e, ok := s.decoded[tag]; ok {
		return e, nil
	}
	raw, ok := s.raw[tag]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrTagNotFound, tag)
	}
	entries, err := decodeGeoSite(raw)
	if err != nil {
		return nil, fmt.Errorf("decode geosite %q: %w", tag, err)
	}
	s.decoded[tag] = entries
	return entries, nil
}

// EntriesUncached decodes a tag without keeping the result in the per-tag
// cache; the reverse index walks every tag once and must not pin all of
// them in memory.
func (s *Site) EntriesUncached(tag string) ([]Entry, error) {
	tag = strings.ToLower(tag)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensure(); err != nil {
		return nil, err
	}
	if e, ok := s.decoded[tag]; ok {
		return e, nil
	}
	raw, ok := s.raw[tag]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrTagNotFound, tag)
	}
	return decodeGeoSite(raw)
}

// ErrTagNotFound is returned for unknown tags.
var ErrTagNotFound = fmt.Errorf("geo tag not found")

// Version returns a string that changes whenever the file changes, suitable
// as a cache key.
func (s *Site) Version() (string, error) {
	st, err := os.Stat(s.path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%d", st.Size(), st.ModTime().UnixNano()), nil
}

func (s *Site) ensure() error {
	st, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	id := fileID{st.Size(), st.ModTime()}
	if s.raw != nil && id == s.id {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	raw, err := splitList(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	s.raw, s.decoded, s.id = raw, map[string][]Entry{}, id
	return nil
}

// splitList walks a GeoSiteList/GeoIPList (field 1 repeated message) and
// indexes each entry's bytes by its lower-cased country_code (field 1 string).
func splitList(data []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]
		if num != 1 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			data = data[n:]
			continue
		}
		msg, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]
		tag, err := readStringField(msg, 1)
		if err != nil {
			return nil, err
		}
		if tag == "" {
			continue
		}
		out[strings.ToLower(tag)] = msg
	}
	return out, nil
}

// readStringField returns the first occurrence of string field num in msg.
func readStringField(msg []byte, want protowire.Number) (string, error) {
	for len(msg) > 0 {
		num, typ, n := protowire.ConsumeTag(msg)
		if n < 0 {
			return "", protowire.ParseError(n)
		}
		msg = msg[n:]
		if num == want && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(msg)
			if n < 0 {
				return "", protowire.ParseError(n)
			}
			return string(v), nil
		}
		n = protowire.ConsumeFieldValue(num, typ, msg)
		if n < 0 {
			return "", protowire.ParseError(n)
		}
		msg = msg[n:]
	}
	return "", nil
}

// decodeGeoSite decodes GeoSite.domain (field 2 repeated Domain).
func decodeGeoSite(msg []byte) ([]Entry, error) {
	var out []Entry
	for len(msg) > 0 {
		num, typ, n := protowire.ConsumeTag(msg)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		msg = msg[n:]
		if num != 2 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, msg)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			msg = msg[n:]
			continue
		}
		dm, n := protowire.ConsumeBytes(msg)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		msg = msg[n:]
		e, err := decodeDomain(dm)
		if err != nil {
			return nil, err
		}
		if e.Value != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

func decodeDomain(msg []byte) (Entry, error) {
	e := Entry{Kind: KindPlain}
	for len(msg) > 0 {
		num, typ, n := protowire.ConsumeTag(msg)
		if n < 0 {
			return e, protowire.ParseError(n)
		}
		msg = msg[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(msg)
			if n < 0 {
				return e, protowire.ParseError(n)
			}
			msg = msg[n:]
			e.Kind = DomainKind(v)
		case num == 2 && typ == protowire.BytesType:
			v, n := protowire.ConsumeBytes(msg)
			if n < 0 {
				return e, protowire.ParseError(n)
			}
			msg = msg[n:]
			e.Value = strings.ToLower(string(v))
		case num == 3 && typ == protowire.BytesType:
			v, n := protowire.ConsumeBytes(msg)
			if n < 0 {
				return e, protowire.ParseError(n)
			}
			msg = msg[n:]
			if key, err := readStringField(v, 1); err == nil && key != "" {
				e.Attrs = append(e.Attrs, key)
			}
		default:
			n = protowire.ConsumeFieldValue(num, typ, msg)
			if n < 0 {
				return e, protowire.ParseError(n)
			}
			msg = msg[n:]
		}
	}
	if e.Kind < KindPlain || e.Kind > KindFull {
		e.Kind = kindUnknown
	}
	return e, nil
}

// IP is a lazily loaded geoip.dat.
type IP struct {
	path string

	mu      sync.Mutex
	id      fileID
	raw     map[string][]byte
	decoded map[string][]netip.Prefix
}

// OpenIP returns a reader for a geoip.dat path.
func OpenIP(path string) *IP { return &IP{path: path} }

// Tags lists every geoip tag, sorted.
func (g *IP) Tags() ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.ensure(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(g.raw))
	for t := range g.raw {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// HasTag reports whether tag exists.
func (g *IP) HasTag(tag string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.ensure(); err != nil {
		return false, err
	}
	_, ok := g.raw[strings.ToLower(tag)]
	return ok, nil
}

// CIDRs returns the prefixes of tag, sorted and masked.
func (g *IP) CIDRs(tag string) ([]netip.Prefix, error) {
	tag = strings.ToLower(tag)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.ensure(); err != nil {
		return nil, err
	}
	if p, ok := g.decoded[tag]; ok {
		return p, nil
	}
	raw, ok := g.raw[tag]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrTagNotFound, tag)
	}
	prefixes, err := decodeGeoIP(raw)
	if err != nil {
		return nil, fmt.Errorf("decode geoip %q: %w", tag, err)
	}
	g.decoded[tag] = prefixes
	return prefixes, nil
}

func (g *IP) ensure() error {
	st, err := os.Stat(g.path)
	if err != nil {
		return err
	}
	id := fileID{st.Size(), st.ModTime()}
	if g.raw != nil && id == g.id {
		return nil
	}
	data, err := os.ReadFile(g.path)
	if err != nil {
		return err
	}
	raw, err := splitList(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", g.path, err)
	}
	g.raw, g.decoded, g.id = raw, map[string][]netip.Prefix{}, id
	return nil
}

// decodeGeoIP decodes GeoIP.cidr (field 2 repeated CIDR{1: ip bytes, 2: prefix}).
func decodeGeoIP(msg []byte) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for len(msg) > 0 {
		num, typ, n := protowire.ConsumeTag(msg)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		msg = msg[n:]
		if num != 2 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, msg)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			msg = msg[n:]
			continue
		}
		cm, n := protowire.ConsumeBytes(msg)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		msg = msg[n:]
		var ip []byte
		var bits uint64
		for len(cm) > 0 {
			fn, ft, k := protowire.ConsumeTag(cm)
			if k < 0 {
				return nil, protowire.ParseError(k)
			}
			cm = cm[k:]
			switch {
			case fn == 1 && ft == protowire.BytesType:
				v, k := protowire.ConsumeBytes(cm)
				if k < 0 {
					return nil, protowire.ParseError(k)
				}
				cm = cm[k:]
				ip = v
			case fn == 2 && ft == protowire.VarintType:
				v, k := protowire.ConsumeVarint(cm)
				if k < 0 {
					return nil, protowire.ParseError(k)
				}
				cm = cm[k:]
				bits = v
			default:
				k = protowire.ConsumeFieldValue(fn, ft, cm)
				if k < 0 {
					return nil, protowire.ParseError(k)
				}
				cm = cm[k:]
			}
		}
		addr, ok := netip.AddrFromSlice(ip)
		if !ok || int(bits) > addr.BitLen() {
			continue
		}
		out = append(out, netip.PrefixFrom(addr, int(bits)).Masked())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Addr() != out[j].Addr() {
			return out[i].Addr().Less(out[j].Addr())
		}
		return out[i].Bits() < out[j].Bits()
	})
	return out, nil
}
