package v2

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/geo"
)

func TestGeoTagsListsInstalledTags(t *testing.T) {
	dir := t.TempDir()
	site := filepath.Join(dir, "geosite.dat")
	ip := filepath.Join(dir, "geoip.dat")
	_ = os.WriteFile(site, geo.EncodeSiteList(map[string][]geo.Entry{
		"NETFLIX": {{Kind: geo.KindRoot, Value: "netflix.com"}},
		"cn":      {{Kind: geo.KindRoot, Value: "baidu.com"}},
	}), 0o644)
	_ = os.WriteFile(ip, geo.EncodeIPList(map[string][]geo.IPEntry{
		"private": {{IP: []byte{10, 0, 0, 0}, Bits: 8}},
	}), 0o644)
	e := newEnvWith(t, func(d *Deps) { d.GeoSite = geo.OpenSite(site); d.GeoIP = geo.OpenIP(ip) })

	code, out := e.do(http.MethodGet, "/api/v2/geo/tags?kind=geosite", nil)
	if code != 200 || out["installed"] != true {
		t.Fatalf("geosite %d %v", code, out)
	}
	items := out["items"].([]any)
	if len(items) != 2 || items[0] != "cn" || items[1] != "netflix" {
		t.Fatalf("geosite tags %v (expected lower-cased, sorted)", items)
	}
	code, out = e.do(http.MethodGet, "/api/v2/geo/tags?kind=geoip", nil)
	if code != 200 || len(out["items"].([]any)) != 1 {
		t.Fatalf("geoip %d %v", code, out)
	}
	if code, _ := e.do(http.MethodGet, "/api/v2/geo/tags?kind=bogus", nil); code != 422 && code != 400 {
		t.Fatalf("bogus kind code %d", code)
	}
}

func TestGeoTagsWithoutGeodata(t *testing.T) {
	e := newEnv(t)
	code, out := e.do(http.MethodGet, "/api/v2/geo/tags?kind=geosite", nil)
	if code != 200 || out["installed"] != false || len(out["items"].([]any)) != 0 {
		t.Fatalf("%d %v", code, out)
	}
}

func TestGeoLookupAndEntries(t *testing.T) {
	dir := t.TempDir()
	site := filepath.Join(dir, "geosite.dat")
	ip := filepath.Join(dir, "geoip.dat")
	_ = os.WriteFile(site, geo.EncodeSiteList(map[string][]geo.Entry{
		"google": {{Kind: geo.KindRoot, Value: "google.com"}, {Kind: geo.KindRoot, Value: "youtube.com"}},
		"cn":     {{Kind: geo.KindRoot, Value: "baidu.com"}},
	}), 0o644)
	_ = os.WriteFile(ip, geo.EncodeIPList(map[string][]geo.IPEntry{
		"google":  {{IP: []byte{142, 250, 0, 0}, Bits: 15}},
		"private": {{IP: []byte{10, 0, 0, 0}, Bits: 8}},
	}), 0o644)
	e := newEnvWith(t, func(d *Deps) {
		d.GeoSite, d.GeoIP = geo.OpenSite(site), geo.OpenIP(ip)
		d.SiteIndex, d.IPIndex = geo.NewSiteIndex(d.GeoSite), geo.NewIPIndex(d.GeoIP)
	})
	code, out := e.do(http.MethodGet, "/api/v2/geo/lookup?target=www.google.com", nil)
	if code != 200 || out["kind"] != "domain" || len(out["geosite"].([]any)) != 1 || out["geosite"].([]any)[0] != "google" {
		t.Fatalf("domain lookup %d %v", code, out)
	}
	code, out = e.do(http.MethodGet, "/api/v2/geo/lookup?target=142.251.1.1:443", nil)
	if code != 200 || out["kind"] != "ip" || out["target"] != "142.251.1.1" || out["geoip"].([]any)[0] != "google" {
		t.Fatalf("ip lookup %d %v", code, out)
	}
	code, out = e.do(http.MethodGet, "/api/v2/geo/entries?kind=geosite&tag=google&q=you", nil)
	if code != 200 || out["total"] != float64(1) || out["items"].([]any)[0] != "domain:youtube.com" {
		t.Fatalf("entries %d %v", code, out)
	}
	code, out = e.do(http.MethodGet, "/api/v2/geo/entries?kind=geoip&tag=private", nil)
	if code != 200 || out["items"].([]any)[0] != "10.0.0.0/8" {
		t.Fatalf("geoip entries %d %v", code, out)
	}
	if code, _ := e.do(http.MethodGet, "/api/v2/geo/entries?kind=geoip&tag=nope", nil); code != 422 {
		t.Fatalf("unknown tag code %d", code)
	}
	if labels := geo.Labels(geo.NewSiteIndex(geo.OpenSite(site)), geo.NewIPIndex(geo.OpenIP(ip)), "10.1.2.3:53"); len(labels) != 1 || labels[0] != "geoip:private" {
		t.Fatalf("labels %v", labels)
	}
}
