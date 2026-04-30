package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHealthCheckAPI(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	root := os.Getenv("PROXYGW_HOME")

	t.Run("health check returns OK for base components", func(t *testing.T) {
		// Mock binaries
		mustMkdirAll(t, filepath.Join(root, "core", "xray"))
		mustMkdirAll(t, filepath.Join(root, "core", "mosdns"))
		mustWriteFile(t, filepath.Join(root, "core", "xray", "xray"), "mock")
		mustWriteFile(t, filepath.Join(root, "core", "mosdns", "mosdns"), "mock")
		
		// Mock geodata (large enough)
		bigData := make([]byte, minHealthyGeodataSize)
		os.WriteFile(filepath.Join(root, "core", "mosdns", "geoip.dat"), bigData, 0644)
		os.WriteFile(filepath.Join(root, "core", "mosdns", "geosite.dat"), bigData, 0644)

		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/test/health_check"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
		
		var resp map[string]any
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp["success"] != true {
			t.Fatal("expected success: true")
		}
		
		results := resp["results"].([]any)
		// We expect 5 results: Database, Xray, Mosdns, GeoData, FRR/OSPF (since mode is B by default in feature suite)
		if len(results) != 5 {
			t.Fatalf("expected 5 components, got %d", len(results))
		}
	})
}
