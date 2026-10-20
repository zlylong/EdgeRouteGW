package geo

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestSiteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "geosite.dat")
	body := EncodeSiteList(map[string][]Entry{
		"CN": {
			{Kind: KindRoot, Value: "Baidu.com"},
			{Kind: KindFull, Value: "www.qq.com", Attrs: []string{"cn"}},
			{Kind: KindPlain, Value: "taobao"},
			{Kind: KindRegex, Value: `^ad\d+\.`},
		},
		"netflix": {{Kind: KindRoot, Value: "netflix.com"}},
	})
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	s := OpenSite(path)
	tags, err := s.Tags()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "cn" || tags[1] != "netflix" {
		t.Fatalf("tags %v", tags)
	}
	ok, _ := s.HasTag("CN")
	if !ok {
		t.Fatal("HasTag should be case-insensitive")
	}
	entries, err := s.Entries("cn")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"domain:baidu.com", "full:www.qq.com", "keyword:taobao", `regexp:^ad\d+\.`}
	if len(entries) != len(want) {
		t.Fatalf("entries %+v", entries)
	}
	for i, e := range entries {
		if e.MosdnsValue() != want[i] {
			t.Fatalf("entry %d: %q want %q", i, e.MosdnsValue(), want[i])
		}
	}
	if len(entries[1].Attrs) != 1 || entries[1].Attrs[0] != "cn" {
		t.Fatalf("attrs %v", entries[1].Attrs)
	}
	if _, err := s.Entries("nope"); err == nil {
		t.Fatal("unknown tag should error")
	}

	// Rewriting the file invalidates the cache.
	body2 := EncodeSiteList(map[string][]Entry{"cn": {{Kind: KindFull, Value: "only.cn"}}})
	if err := os.WriteFile(path, body2, 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure the mtime/size differs (size does here).
	entries, err = s.Entries("cn")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Value != "only.cn" {
		t.Fatalf("stale cache: %+v", entries)
	}
}

func TestIPRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.dat")
	body := EncodeIPList(map[string][]IPEntry{
		"cn": {{IP: []byte{1, 2, 3, 77}, Bits: 24}, {IP: []byte{1, 0, 0, 0}, Bits: 8}},
		"v6": {{IP: netip.MustParseAddr("2001:db8::1").AsSlice(), Bits: 32}},
	})
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	g := OpenIP(path)
	cidrs, err := g.CIDRs("cn")
	if err != nil {
		t.Fatal(err)
	}
	if len(cidrs) != 2 || cidrs[0].String() != "1.0.0.0/8" || cidrs[1].String() != "1.2.3.0/24" {
		t.Fatalf("cidrs %v", cidrs)
	}
	v6, _ := g.CIDRs("v6")
	if len(v6) != 1 || v6[0].String() != "2001:db8::/32" {
		t.Fatalf("v6 %v", v6)
	}
}

func TestTruncatedFileDoesNotPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geosite.dat")
	body := EncodeSiteList(map[string][]Entry{"cn": {{Kind: KindRoot, Value: "baidu.com"}}})
	if err := os.WriteFile(path, body[:len(body)-3], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSite(path).Tags(); err == nil {
		t.Fatal("truncated file should produce an error")
	}
}
