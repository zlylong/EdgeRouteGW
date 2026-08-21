package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLanACLCreateValidatesInput(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	reject := []struct {
		name string
		body string
	}{
		{"bad mac", `{"type":"mac","value":"not-a-mac","policy":"proxy"}`},
		{"bad ip", `{"type":"ip","value":"999.999.1.1","policy":"direct"}`},
		{"template injection", `{"type":"ip","value":"1.1.1.1 }; table inet evil {}","policy":"direct"}`},
		{"bad type", `{"type":"host","value":"1.1.1.1","policy":"proxy"}`},
		{"bad policy", `{"type":"ip","value":"1.1.1.1","policy":"evil"}`},
	}
	for _, tc := range reject {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/lan_acls", tc.body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400 got %d body=%s", tc.name, w.Code, w.Body.String())
		}
	}

	// Valid MAC/IP must pass validation (they then fail later at nft apply,
	// since nft is unavailable in tests — so we only assert they are NOT 400).
	// IPv6 and private-supernet CIDRs are valid LAN ACL inputs: the nft
	// template renders separate ip6 sets and interval sets in applyNftablesConfig.
	accept := []string{
		`{"type":"mac","value":"aa:bb:cc:dd:ee:ff","policy":"proxy"}`,
		`{"type":"ip","value":"1.2.3.4/32","policy":"direct"}`,
		`{"type":"ip","value":"192.168.0.0/16","policy":"direct"}`,
		`{"type":"ip","value":"10.0.0.0/8","policy":"proxy"}`,
		`{"type":"ip","value":"2001:db8::/64","policy":"direct"}`,
		`{"type":"ip","value":"fd00::1","policy":"proxy"}`,
	}
	for _, body := range accept {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/lan_acls", body))
		if w.Code == http.StatusBadRequest {
			t.Fatalf("valid input wrongly rejected: body=%s resp=%s", body, w.Body.String())
		}
	}
}
