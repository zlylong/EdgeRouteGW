package main

import (
	"container/list"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type testGeoIPEntry struct {
	Tag   string
	CIDRs []struct {
		IP     []byte
		Prefix int
	}
}

type testGeoSiteDomain struct {
	Type  int
	Value string
}

type testGeoSiteEntry struct {
	Tag     string
	Domains []testGeoSiteDomain
}

func encodeVarint(v int) []byte {
	if v == 0 {
		return []byte{0}
	}
	var out []byte
	for v > 0 {
		b := byte(v & 0x7F)
		v >>= 7
		if v > 0 {
			b |= 0x80
		}
		out = append(out, b)
	}
	return out
}

func encodeLenField(field byte, payload []byte) []byte {
	out := []byte{field}
	out = append(out, encodeVarint(len(payload))...)
	out = append(out, payload...)
	return out
}

func buildTestGeoIPDat(entries []testGeoIPEntry) []byte {
	var out []byte
	for _, entry := range entries {
		var msg []byte
		msg = append(msg, encodeLenField(0x0A, []byte(entry.Tag))...)
		for _, cidr := range entry.CIDRs {
			var cidrMsg []byte
			cidrMsg = append(cidrMsg, encodeLenField(0x0A, cidr.IP)...)
			cidrMsg = append(cidrMsg, 0x10)
			cidrMsg = append(cidrMsg, encodeVarint(cidr.Prefix)...)
			msg = append(msg, encodeLenField(0x12, cidrMsg)...)
		}
		out = append(out, encodeLenField(0x0A, msg)...)
	}
	return out
}

func buildTestGeoSiteDat(entries []testGeoSiteEntry) []byte {
	var out []byte
	for _, entry := range entries {
		var msg []byte
		msg = append(msg, encodeLenField(0x0A, []byte(entry.Tag))...)
		for _, domain := range entry.Domains {
			var domainMsg []byte
			domainMsg = append(domainMsg, 0x08)
			domainMsg = append(domainMsg, encodeVarint(domain.Type)...)
			domainMsg = append(domainMsg, encodeLenField(0x12, []byte(domain.Value))...)
			msg = append(msg, encodeLenField(0x12, domainMsg)...)
		}
		out = append(out, encodeLenField(0x0A, msg)...)
	}
	return out
}

func writeTestGeoData(t *testing.T) {
	t.Helper()
	geoip := buildTestGeoIPDat([]testGeoIPEntry{
		{Tag: "cn", CIDRs: []struct {
			IP     []byte
			Prefix int
		}{{IP: []byte{1, 1, 1, 0}, Prefix: 24}}},
		{Tag: "google", CIDRs: []struct {
			IP     []byte
			Prefix int
		}{{IP: []byte{8, 8, 8, 0}, Prefix: 24}}},
	})
	geosite := buildTestGeoSiteDat([]testGeoSiteEntry{
		{Tag: "gfw", Domains: []testGeoSiteDomain{{Type: 2, Value: "google.com"}, {Type: 3, Value: "youtube.com"}}},
		{Tag: "google", Domains: []testGeoSiteDomain{{Type: 2, Value: "google.com"}}},
		{Tag: "cn", Domains: []testGeoSiteDomain{{Type: 2, Value: "baidu.com"}}},
	})
	if err := os.WriteFile(getPath("core", "mosdns", "geoip.dat"), geoip, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(getPath("core", "mosdns", "geosite.dat"), geosite, 0o644); err != nil {
		t.Fatal(err)
	}
	cacheMutex.Lock()
	cachedGeoip = nil
	cachedGeosite = nil
	cacheMutex.Unlock()

	geoIPMatcherMu.Lock()
	geoIPMatcherCache = map[string]*geoIPMatcher{}
	geoIPMatcherMu.Unlock()

	geoSiteMatcherMu.Lock()
	geoSiteMatcherCache = map[string]*geoSiteMatcher{}
	geoSiteMatcherMu.Unlock()

	geoIPTagCacheMu.Lock()
	geoIPTagCacheList = list.New()
	geoIPTagCacheMap = map[geoIPCacheKey]*list.Element{}
	geoIPTagCacheMu.Unlock()

	geoSiteTagMatchCacheMu.Lock()
	geoSiteTagMatchCacheList = list.New()
	geoSiteTagMatchCacheMap = map[geoSiteTagMatchCacheKey]*list.Element{}
	geoSiteTagMatchCacheMu.Unlock()
}

func TestGeoQueryLookupAndExpand(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	writeTestGeoData(t)

	t.Run("domain lookup returns matching geosite tags and resolved geoip tags", func(t *testing.T) {
		oldLookup := geoQueryLookupIP
		geoQueryLookupIP = func(host string) ([]string, error) {
			if host != "www.google.com" {
				t.Fatalf("unexpected host lookup: %s", host)
			}
			return []string{"8.8.8.8", "1.1.1.1"}, nil
		}
		defer func() { geoQueryLookupIP = oldLookup }()

		w := httptest.NewRecorder()
		req := authedRequest(http.MethodGet, "/api/geo/query?input=www.google.com")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		if resp["mode"].(string) != "lookup" {
			t.Fatalf("unexpected response: %v", resp)
		}
		matches := resp["geosite_matches"].([]interface{})
		if len(matches) != 2 || matches[0].(string) != "gfw" || matches[1].(string) != "google" {
			t.Fatalf("unexpected geosite matches: %v", resp)
		}
		resolved := resp["resolved_ips"].([]interface{})
		if len(resolved) != 2 || resolved[0].(string) != "1.1.1.1" || resolved[1].(string) != "8.8.8.8" {
			t.Fatalf("unexpected resolved ips: %v", resp)
		}
		geoipMatches := resp["geoip_matches"].([]interface{})
		if len(geoipMatches) != 2 || geoipMatches[0].(string) != "cn" || geoipMatches[1].(string) != "google" {
			t.Fatalf("unexpected geoip matches from dns lookup: %v", resp)
		}
	})

	t.Run("ip lookup returns matching geoip tags", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := authedRequest(http.MethodGet, "/api/geo/query?input=8.8.8.8")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		matches := resp["geoip_matches"].([]interface{})
		if len(matches) != 1 || matches[0].(string) != "google" {
			t.Fatalf("unexpected geoip matches: %v", resp)
		}
	})

	t.Run("geoip expand returns cidrs and validates existence", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := authedRequest(http.MethodGet, "/api/geo/query?input=geoip:cn")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		if resp["mode"].(string) != "expand" || resp["exists"].(bool) != true {
			t.Fatalf("unexpected expand response: %v", resp)
		}
		values := resp["values"].([]interface{})
		if len(values) != 1 || values[0].(string) != "1.1.1.0/24" {
			t.Fatalf("unexpected geoip extracted values: %v", resp)
		}
	})

	t.Run("geosite expand returns typed entries and validates existence", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := authedRequest(http.MethodGet, "/api/geo/query?input=geosite:gfw")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		if resp["mode"].(string) != "expand" || resp["exists"].(bool) != true {
			t.Fatalf("unexpected expand response: %v", resp)
		}
		values := resp["values"].([]interface{})
		if len(values) != 2 {
			t.Fatalf("unexpected geosite extracted values: %v", resp)
		}
	})
}

func TestGeoSiteTagMatchesDomain(t *testing.T) {
	writeTestGeoData(t)
	geositePath := getPath("core", "mosdns", "geosite.dat")

	if !geoSiteTagMatchesDomain(geositePath, "gfw", "www.google.com") {
		t.Fatalf("expected gfw tag to match www.google.com")
	}
	if !geoSiteTagMatchesDomain(geositePath, "gfw", "youtube.com") {
		t.Fatalf("expected gfw tag to match full youtube.com rule")
	}
	if geoSiteTagMatchesDomain(geositePath, "cn", "www.google.com") {
		t.Fatalf("did not expect cn tag to match www.google.com")
	}
	// second pass verifies hot-path cache lookup correctness
	if !geoSiteTagMatchesDomain(geositePath, "gfw", "www.google.com") {
		t.Fatalf("expected cached gfw tag match to remain true")
	}
}
