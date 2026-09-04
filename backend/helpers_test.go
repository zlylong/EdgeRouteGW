package main

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestFormatUpstreamsLocalSingle(t *testing.T) {
	got := formatUpstreams("119.29.29.29,223.5.5.5", false)
	want := `[{ addr: "119.29.29.29" }, { addr: "223.5.5.5" }]`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestFormatUpstreamsRemoteWithSocks(t *testing.T) {
	got := formatUpstreams("1.1.1.1,8.8.8.8", true)
	want := `[{ addr: "tcp://1.1.1.1", socks5: "127.0.0.1:10808" }, { addr: "tcp://8.8.8.8", socks5: "127.0.0.1:10808" }]`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestFormatUpstreamsTrimAndSplit(t *testing.T) {
	got := formatUpstreams(" 1.1.1.1 , , 8.8.8.8 ", false)
	want := `[{ addr: "1.1.1.1" }, { addr: "8.8.8.8" }]`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestFormatUpstreamsFallbackLocal(t *testing.T) {
	got := formatUpstreams("", false)
	want := `[{ addr: "119.29.29.29" }, { addr: "223.5.5.5" }]`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestFormatUpstreamsFallbackRemote(t *testing.T) {
	got := formatUpstreams("", true)
	want := `[{ addr: "tcp://1.1.1.1", socks5: "127.0.0.1:10808" }, { addr: "tcp://8.8.8.8", socks5: "127.0.0.1:10808" }]`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

// The project ships arm64 releases and install.sh handles aarch64, so these
// must not assume the host that runs the tests is amd64. Deriving the asset the
// same way the implementation does keeps them honest on both architectures
// while still pinning the URL shape, which is the part that actually matters.
func TestBuildXrayDownloadURLLatest(t *testing.T) {
	asset, err := xrayAssetName()
	if err != nil {
		t.Skipf("no Xray asset for %s", runtime.GOARCH)
	}
	got, err := buildXrayDownloadURL("latest")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/XTLS/Xray-core/releases/latest/download/" + asset
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestBuildXrayDownloadURLEmpty(t *testing.T) {
	asset, err := xrayAssetName()
	if err != nil {
		t.Skipf("no Xray asset for %s", runtime.GOARCH)
	}
	got, err := buildXrayDownloadURL("")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/XTLS/Xray-core/releases/latest/download/" + asset
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestBuildXrayDownloadURLSpecific(t *testing.T) {
	asset, err := xrayAssetName()
	if err != nil {
		t.Skipf("no Xray asset for %s", runtime.GOARCH)
	}
	got, err := buildXrayDownloadURL("v26.3.27")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/XTLS/Xray-core/releases/download/v26.3.27/" + asset
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

// The mapping itself is what the URL tests can no longer catch, so pin it here.
func TestXrayAssetNamePerArch(t *testing.T) {
	switch runtime.GOARCH {
	case "amd64":
		if a, _ := xrayAssetName(); a != "Xray-linux-64.zip" {
			t.Fatalf("amd64 asset is %q", a)
		}
	case "arm64":
		if a, _ := xrayAssetName(); a != "Xray-linux-arm64-v8a.zip" {
			t.Fatalf("arm64 asset is %q", a)
		}
	default:
		if _, err := xrayAssetName(); err == nil {
			t.Fatalf("unsupported arch %s should not yield an asset", runtime.GOARCH)
		}
	}
}

func TestBuildXrayDownloadURLRejectInvalid(t *testing.T) {
	if _, err := buildXrayDownloadURL("v26.3.27;rm -rf /"); err == nil {
		t.Fatal("expected invalid version error")
	}
}

func TestParseXrayVersionOutput(t *testing.T) {
	got := parseXrayVersionOutput("Xray 26.3.27 (Xray, Penetrates Everything.)\nA unified platform")
	if got != "26.3.27" {
		t.Fatalf("want 26.3.27, got %s", got)
	}
}

func TestParsePortValue(t *testing.T) {
	if got := parsePortValue(float64(8443)); got != 8443 {
		t.Fatalf("want 8443, got %d", got)
	}
	if got := parsePortValue("2053"); got != 2053 {
		t.Fatalf("want 2053, got %d", got)
	}
	if got := parsePortValue("bad"); got != 443 {
		t.Fatalf("want 443, got %d", got)
	}
}

func TestIsValidIPOrCIDR(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"8.8.8.8", true},
		{"8.8.8.0/24", true},
		{"0.0.0.0", false},
		{"0.0.0.0/0", false},
		{"127.0.0.1", false},
		{"169.254.10.1", false},
		{"224.0.0.1", false},
		{"not-an-ip", false},
	}
	for _, tc := range cases {
		if got := isValidIPOrCIDR(tc.in); got != tc.want {
			t.Fatalf("isValidIPOrCIDR(%q)=%v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestAuthMiddlewareUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sessions.Store("abc", SessionInfo{ExpiresAt: time.Now().Add(time.Hour)})
	r := gin.New()
	r.Use(authMiddleware)
	r.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuthMiddlewareAuthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sessions.Store("abc", SessionInfo{ExpiresAt: time.Now().Add(time.Hour)})
	r := gin.New()
	r.Use(authMiddleware)
	r.GET("/ok", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("Authorization", "Bearer abc")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}
