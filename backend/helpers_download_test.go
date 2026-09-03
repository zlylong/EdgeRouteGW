package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildXrayDownloadURLMatchesRunningArchitecture(t *testing.T) {
	wantAsset, err := xrayAssetName()
	if err != nil {
		t.Skipf("architecture %s is not a release target", runtime.GOARCH)
	}

	// The asset used to be hardcoded to Xray-linux-64.zip, so an in-app update
	// on an arm64 gateway installed a binary the machine cannot execute.
	latest, err := buildXrayDownloadURL("latest")
	if err != nil {
		t.Fatalf("buildXrayDownloadURL(latest) error: %v", err)
	}
	if !strings.HasSuffix(latest, "/"+wantAsset) {
		t.Errorf("latest URL = %s, want it to end in %s", latest, wantAsset)
	}

	pinned, err := buildXrayDownloadURL("v1.8.24")
	if err != nil {
		t.Fatalf("buildXrayDownloadURL(v1.8.24) error: %v", err)
	}
	if !strings.HasSuffix(pinned, "/"+wantAsset) {
		t.Errorf("pinned URL = %s, want it to end in %s", pinned, wantAsset)
	}
	if !strings.Contains(pinned, "/download/v1.8.24/") {
		t.Errorf("pinned URL = %s, want the requested tag in the path", pinned)
	}
}

// badVersions are tags that must never be interpolated into a download URL:
// each could retarget it at another path or another repository.
var badVersions = []string{
	"v1/../../../../attacker/evil/releases/download/v1",
	"../../attacker/evil",
	"v1.0.0/../../..",
	"v1 v2",
	"v1\nv2",
	"v1?x=y",
	"v1#frag",
	"v1@evil.com",
	"https://evil.example.com/x",
	"latest/../..",
}

func TestBuildXrayDownloadURLRejectsMalformedVersions(t *testing.T) {
	if _, err := xrayAssetName(); err != nil {
		t.Skipf("architecture %s is not a release target", runtime.GOARCH)
	}
	for _, v := range badVersions {
		if url, err := buildXrayDownloadURL(v); err == nil {
			t.Errorf("buildXrayDownloadURL(%q) = %s, want error", v, url)
		}
	}
}

func TestBuildMosdnsDownloadURLRejectsMalformedVersions(t *testing.T) {
	arch, err := mosdnsArch()
	if err != nil {
		t.Skipf("architecture %s is not a release target", runtime.GOARCH)
	}

	// This builder had no validation at all, unlike its Xray counterpart.
	for _, v := range badVersions {
		if url, err := buildMosdnsDownloadURL(v); err == nil {
			t.Errorf("buildMosdnsDownloadURL(%q) = %s, want error", v, url)
		}
	}
	for _, v := range []string{"", "   ", "latest"} {
		if url, err := buildMosdnsDownloadURL(v); err == nil {
			t.Errorf("buildMosdnsDownloadURL(%q) = %s, want error", v, url)
		}
	}

	good, err := buildMosdnsDownloadURL("v5.3.4")
	if err != nil {
		t.Fatalf("buildMosdnsDownloadURL(v5.3.4) error: %v", err)
	}
	want := "https://github.com/IrineSistiana/mosdns/releases/download/v5.3.4/mosdns-linux-" + arch + ".zip"
	if good != want {
		t.Errorf("buildMosdnsDownloadURL(v5.3.4) = %s, want %s", good, want)
	}
}

func TestGetMosdnsHashRejectsMalformedVersionsWithoutNetwork(t *testing.T) {
	if _, err := mosdnsArch(); err != nil {
		t.Skipf("architecture %s is not a release target", runtime.GOARCH)
	}
	// Validation must happen before the release lookup, so these never reach
	// the network even when it is unavailable.
	for _, v := range append(append([]string{}, badVersions...), "", "latest") {
		if hash, err := getMosdnsHash(v); err == nil {
			t.Errorf("getMosdnsHash(%q) = %s, want error", v, hash)
		}
	}
}

func TestDownloadWithVerificationRejectsBlankExpectedHash(t *testing.T) {
	// The mosdns update path passed "" here, which took the fail-open branch and
	// installed whatever came back over the wire as a root-run binary.
	if err := downloadWithVerification("https://example.invalid/x.zip", t.TempDir()+"/x.zip", ""); err == nil {
		t.Error("downloadWithVerification with a blank hash = nil, want error")
	}
}
