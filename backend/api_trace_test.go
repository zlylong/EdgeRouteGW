package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceAPI(t *testing.T) {
	r := setupFeatureSuiteRouter(t) // Reuse feature suite for rich data
	
	root := os.Getenv("PROXYGW_HOME")
	writeTestGeoData_Local(t, root)

	t.Run("trace domain match explicit rule", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/test/trace?target=example.com"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["outbound"] != "proxy-2-out" {
			t.Fatalf("unexpected outbound: %v", resp["outbound"])
		}
		rule := resp["matched_rule"].(map[string]any)
		if rule["value"] != "example.com" {
			t.Fatalf("unexpected matched rule: %v", rule["value"])
		}
	})

	t.Run("trace domain match geosite", func(t *testing.T) {
		// Seed a geosite rule
		db.Exec("INSERT INTO rules(type, value, policy, priority) VALUES ('geosite', 'google', 'proxy-1', -10)")
		
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/test/trace?target=www.google.com"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["outbound"] != "proxy-1-out" {
			t.Fatalf("unexpected outbound: %v", resp["outbound"])
		}
		if !strings.Contains(resp["reason"].(string), "geosite:google") {
			t.Fatalf("unexpected reason: %v", resp["reason"])
		}
	})

	t.Run("trace IP match geoip", func(t *testing.T) {
		// Seed a geoip rule
		db.Exec("INSERT INTO rules(type, value, policy, priority) VALUES ('geoip', 'cn', 'direct', -20)")
		
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/test/trace?target=1.1.1.1"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["outbound"] != "direct" {
			t.Fatalf("unexpected outbound: %v", resp["outbound"])
		}
		if !strings.Contains(resp["reason"].(string), "geoip:cn") {
			t.Fatalf("unexpected reason: %v", resp["reason"])
		}
	})

	t.Run("trace fallback to default policy", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/test/trace?target=unknown-site.org"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		// Default policy is 'proxy' in setupFeatureSuiteRouter, default node is 2
		if resp["outbound"] != "proxy-2-out" {
			t.Fatalf("unexpected fallback outbound: %v", resp["outbound"])
		}
		if resp["matched_rule"] != nil {
			t.Fatalf("expected no matched rule, got %v", resp["matched_rule"])
		}
	})
}

func writeTestGeoData_Local(t *testing.T, root string) {
	geoPath := filepath.Join(root, "core", "mosdns")
	os.MkdirAll(geoPath, 0755)
	writeTestGeoData(t) 
}
