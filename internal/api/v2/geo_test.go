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
