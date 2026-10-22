package geo

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexes(t *testing.T) {
	dir := t.TempDir()
	site := filepath.Join(dir, "geosite.dat")
	ip := filepath.Join(dir, "geoip.dat")
	_ = os.WriteFile(site, EncodeSiteList(map[string][]Entry{
		"google": {{Kind: KindRoot, Value: "google.com"}, {Kind: KindRoot, Value: "youtube.com"}},
		"cn":     {{Kind: KindRoot, Value: "baidu.com"}, {Kind: KindFull, Value: "exact.cn"}},
		"ads":    {{Kind: KindPlain, Value: "adserver"}},
		"rx":     {{Kind: KindRegex, Value: `^tracker\d+\.`}},
	}), 0o644)
	_ = os.WriteFile(ip, EncodeIPList(map[string][]IPEntry{
		"google":  {{IP: []byte{142, 250, 0, 0}, Bits: 15}},
		"private": {{IP: []byte{10, 0, 0, 0}, Bits: 8}, {IP: []byte{192, 168, 0, 0}, Bits: 16}},
		"nested":  {{IP: []byte{10, 0, 0, 0}, Bits: 8}, {IP: []byte{10, 1, 0, 0}, Bits: 16}, {IP: []byte{10, 1, 2, 0}, Bits: 24}},
	}), 0o644)
	si := NewSiteIndex(OpenSite(site))
	for in, want := range map[string]string{
		"www.google.com":        "google",
		"m.youtube.com.":        "google",
		"exact.cn":              "cn",
		"sub.exact.cn":          "",
		"adserver.example.org":  "ads",
		"tracker12.example.net": "rx",
		"WWW.BAIDU.COM":         "cn",
	} {
		got, err := si.Lookup(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("site %s -> %v, want %q", in, got, want)
		}
	}
	ii := NewIPIndex(OpenIP(ip))
	for in, want := range map[string]string{
		"142.251.1.1": "google",
		"10.1.2.3":    "nested,private",
		"10.9.9.9":    "nested,private",
		"192.168.1.1": "private",
		"8.8.8.8":     "",
	} {
		got, err := ii.Lookup(netip.MustParseAddr(in))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("ip %s -> %v, want %q", in, got, want)
		}
	}
}
