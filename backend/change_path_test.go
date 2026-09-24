package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeXrayBinary makes "xray -test" and friends succeed inside the feature
// suite home by pointing the binary at /bin/true.
func fakeXrayBinary(t *testing.T) {
	t.Helper()
	p := getPath("core", "xray", "xray")
	_ = os.Remove(p)
	if err := os.Symlink("/bin/true", p); err != nil {
		t.Fatal(err)
	}
}

func swapMosdnsSeams(t *testing.T, active bool) *int32 {
	t.Helper()
	var restarts int32
	oldRestart, oldActive := restartMosdnsFn, unitActiveFn
	restartMosdnsFn = func() error { atomic.AddInt32(&restarts, 1); return nil }
	unitActiveFn = func(unit string) bool { return active }
	lastMosdnsRestartMu.Lock()
	lastMosdnsRestartAt = time.Time{}
	lastMosdnsRestartMu.Unlock()
	t.Cleanup(func() {
		restartMosdnsFn, unitActiveFn = oldRestart, oldActive
		lastMosdnsRestartMu.Lock()
		lastMosdnsRestartAt = time.Time{}
		lastMosdnsRestartMu.Unlock()
	})
	return &restarts
}

func TestApplyMosdnsConfigRestartsOnlyWhenArtifactsChange(t *testing.T) {
	setupFeatureSuiteRouter(t)
	restarts := swapMosdnsSeams(t, true)

	if err := applyMosdnsConfig(); err != nil {
		t.Fatal(err)
	}
	if err := applyMosdnsConfig(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(restarts); got != 1 {
		t.Fatalf("two identical applies restarted mosdns %d times, want 1", got)
	}

	// A direct-policy domain rule does not enter proxy_domains.txt.
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('domain', 'intranet.example', 'direct')"); err != nil {
		t.Fatal(err)
	}
	if err := applyMosdnsConfig(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(restarts); got != 1 {
		t.Fatalf("direct rule caused a restart (%d)", got)
	}

	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('domain', 'new-proxy.example', 'proxy')"); err != nil {
		t.Fatal(err)
	}
	if err := applyMosdnsConfig(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(restarts); got != 2 {
		t.Fatalf("proxy rule did not cause a restart (%d)", got)
	}
	body, _ := os.ReadFile(getPath("core", "mosdns", "proxy_domains.txt"))
	if !strings.Contains(string(body), "full:new-proxy.example") {
		t.Fatalf("proxy_domains.txt not updated: %s", body)
	}

	// Unit down: restart even though nothing changed.
	unitActiveFn = func(string) bool { return false }
	if err := applyMosdnsConfig(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(restarts); got != 3 {
		t.Fatalf("inactive unit was not restarted (%d)", got)
	}
}

func TestModeBApplyDoesNotRestartMosdnsTwice(t *testing.T) {
	setupFeatureSuiteRouter(t)
	fakeXrayBinary(t)
	restarts := swapMosdnsSeams(t, true)
	var xrayRestarts int32
	oldXray := restartXrayFn
	restartXrayFn = func() error { atomic.AddInt32(&xrayRestarts, 1); return nil }
	defer func() { restartXrayFn = oldXray }()

	// Force a change so applyMosdnsConfig restarts.
	_ = os.Remove(getPath("core", "mosdns", "config.yaml"))
	if err := applyMosdnsConfig(); err != nil {
		t.Fatal(err)
	}
	if err := applyXrayConfig(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(restarts); got != 1 {
		t.Fatalf("mosdns restarted %d times across an apply in Mode B, want 1", got)
	}
	if got := atomic.LoadInt32(&xrayRestarts); got != 1 {
		t.Fatalf("xray restarted %d times, want 1", got)
	}
}

func TestStartupXrayApplySkipsRestartWhenUnchanged(t *testing.T) {
	setupFeatureSuiteRouter(t)
	fakeXrayBinary(t)
	swapMosdnsSeams(t, true)
	var xrayRestarts int32
	oldXray := restartXrayFn
	restartXrayFn = func() error { atomic.AddInt32(&xrayRestarts, 1); return nil }
	defer func() { restartXrayFn = oldXray }()
	t.Setenv("PROXYGW_FORCE_RESTART_ON_BOOT", "")

	// First boot: config.json on disk is the placeholder -> differs -> restart.
	if err := applyXrayConfigOnStartup(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&xrayRestarts); got != 1 {
		t.Fatalf("changed config did not restart xray (%d)", got)
	}
	// Second boot with an identical config and an active unit: no restart.
	if err := applyXrayConfigOnStartup(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&xrayRestarts); got != 1 {
		t.Fatalf("unchanged config restarted xray on boot (%d)", got)
	}
	// Explicit apply always restarts.
	if err := applyXrayConfig(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&xrayRestarts); got != 2 {
		t.Fatalf("explicit apply did not restart (%d)", got)
	}
	t.Setenv("PROXYGW_FORCE_RESTART_ON_BOOT", "1")
	if err := applyXrayConfigOnStartup(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&xrayRestarts); got != 3 {
		t.Fatalf("PROXYGW_FORCE_RESTART_ON_BOOT=1 did not force a restart (%d)", got)
	}
}

func TestScheduleApplyDoesNotBlockWhileApplyMutexHeld(t *testing.T) {
	setupFeatureSuiteRouter(t)
	applyMutex.Lock()
	done := make(chan struct{})
	go func() {
		scheduleApplyWithMosdns(false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		applyMutex.Unlock()
		t.Fatal("scheduleApplyWithMosdns blocked behind applyMutex")
	}
	applyMutex.Unlock()
	applyTimerMu.Lock()
	if applyTimer != nil {
		applyTimer.Stop()
		applyTimer = nil
	}
	pendingMosdnsApply = false
	applyTimerMu.Unlock()
}

// legacy scan-based geosite expansion, kept in the test as the oracle.
func legacyExtractGeoSiteValues(filename, targetTag string) []string {
	targetTag = strings.ToLower(strings.TrimSpace(targetTag))
	values := make([]string, 0)
	scanGeoSiteEntries(filename, func(tag string, entries []geoSiteDomainEntry) {
		if tag != targetTag {
			return
		}
		for _, entry := range entries {
			values = append(values, entry.Type+":"+entry.Value)
		}
	})
	return values
}

func TestMatcherBackedGeoSiteHelpersMatchLegacyScan(t *testing.T) {
	setupFeatureSuiteRouter(t)
	geosite := buildTestGeoSiteDat([]testGeoSiteEntry{
		{Tag: "Mixed", Domains: []testGeoSiteDomain{
			{Type: 2, Value: "Google.COM"},
			{Type: 3, Value: "youtube.com."},
			{Type: 0, Value: "netflix"},
			{Type: 1, Value: `^ads?\d+\.Example\.com$`},
			{Type: 1, Value: `[invalid`},
			{Type: 2, Value: "google.com"},
		}},
		{Tag: "cn", Domains: []testGeoSiteDomain{{Type: 2, Value: "baidu.com"}}},
	})
	path := getPath("core", "mosdns", "geosite.dat")
	if err := os.WriteFile(path, geosite, 0o644); err != nil {
		t.Fatal(err)
	}
	geoSiteMatcherMu.Lock()
	geoSiteMatcherCache = map[string]*geoSiteMatcher{}
	geoSiteMatcherMu.Unlock()

	for _, tag := range []string{"mixed", "MIXED", "cn", "missing"} {
		want := legacyExtractGeoSiteValues(path, tag)
		got := extractGeoSiteValues(path, tag)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("extractGeoSiteValues(%q): got %v want %v", tag, got, want)
		}
		if hasGeoSiteTag(path, tag) != (len(want) > 0) {
			t.Fatalf("hasGeoSiteTag(%q) disagrees with scan", tag)
		}
	}
	domains, skipped, err := extractGeoSiteResolvableDomains(path, "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(domains, []string{"google.com", "youtube.com"}) || skipped != 3 {
		t.Fatalf("resolvable domains = %v skipped=%d", domains, skipped)
	}
	if _, _, err := extractGeoSiteResolvableDomains(filepath.Join(t.TempDir(), "absent.dat"), "cn"); err == nil {
		t.Fatal("missing file must be reported as an error")
	}
}

func TestMatcherBackedGeoIPExpansionMatchesLegacyScan(t *testing.T) {
	setupFeatureSuiteRouter(t)
	writeTestGeoData(t)
	path := getPath("core", "mosdns", "geoip.dat")
	for _, tag := range []string{"cn", "CN", "google", "fastly", "missing"} {
		want := extractGeoIPsScan(path, tag)
		got := extractGeoIPs(path, tag)
		if len(want) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("extractGeoIPs(%q): got %v want %v", tag, got, want)
		}
	}
	want := extractGeoIPsExcludeScan(path, "cn", "private")
	got := extractGeoIPsExclude(path, "!cn", "private")
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("extractGeoIPsExclude: got %v want %v", got, want)
	}
	if len(got) != 2 {
		t.Fatalf("expected google+fastly CIDRs, got %v", got)
	}
}

func TestLegacyGeoIPScanSurvivesTruncatedFile(t *testing.T) {
	setupFeatureSuiteRouter(t)
	writeTestGeoData(t)
	path := getPath("core", "mosdns", "geoip.dat")
	data, _ := os.ReadFile(path)
	trunc := filepath.Join(t.TempDir(), "trunc.dat")
	for cut := 1; cut < len(data); cut += 3 {
		if err := os.WriteFile(trunc, data[:cut], 0o644); err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("cut=%d: parser panicked: %v", cut, r)
				}
			}()
			_ = extractGeoIPsScan(trunc, "cn")
			_ = extractGeoIPsExcludeScan(trunc, "cn")
			_, _ = buildGeoIPMatcher(trunc, "v")
		}()
	}
}

func TestNodeToggleOnlyResyncsItsOwnOutbound(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	fakeXrayBinary(t)
	var calls [][]string
	var adoTags [][]string
	old := runXrayAPI
	runXrayAPI = func(args ...string) commandResult {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "ado" {
			raw, _ := os.ReadFile(args[3])
			var payload struct {
				Outbounds []map[string]interface{} `json:"outbounds"`
			}
			_ = json.Unmarshal(raw, &payload)
			var tags []string
			for _, ob := range payload.Outbounds {
				tags = append(tags, ob["tag"].(string))
			}
			adoTags = append(adoTags, tags)
		}
		return commandResult{}
	}
	defer func() { runXrayAPI = old }()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPut, "/api/nodes/1/toggle"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var rmo []string
	sawAdrules := false
	for _, c := range calls {
		switch c[0] {
		case "rmo":
			rmo = c[3:]
		case "adrules":
			sawAdrules = true
		}
	}
	if !reflect.DeepEqual(rmo, []string{"proxy-1-out"}) {
		t.Fatalf("rmo removed %v, want only the toggled node's outbound", rmo)
	}
	if !sawAdrules {
		t.Fatal("routing rules were not refreshed")
	}
	// Inactive nodes keep their outbound in the rendered config (routing
	// simply stops referencing them), so the re-add carries that one tag and
	// never the other node's.
	if len(adoTags) != 1 || !reflect.DeepEqual(adoTags[0], []string{"proxy-1-out"}) {
		t.Fatalf("ado re-added %v, want only [proxy-1-out]", adoTags)
	}

	// Toggle back on: same targeted sequence.
	calls, adoTags = nil, nil
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPut, "/api/nodes/1/toggle"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if len(adoTags) != 1 || !reflect.DeepEqual(adoTags[0], []string{"proxy-1-out"}) {
		t.Fatalf("ado after re-enable = %v, want only [proxy-1-out]", adoTags)
	}
}

func TestFilteredOutboundsForTags(t *testing.T) {
	setupFeatureSuiteRouter(t)
	cfg := `{"outbounds":[{"tag":"direct"},{"tag":"proxy-1-out","protocol":"vless"},{"tag":"proxy-2-out","protocol":"vmess"},{"tag":"proxy-3-out"}]}`
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	all, err := loadProxyOutboundsFromConfigFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("proxy outbounds = %d want 3", len(all))
	}
}

func TestLanAclChangesAreRolledBackWhenApplyFails(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	old := applyNftablesConfigFn
	applyNftablesConfigFn = func() error { return errors.New("nft: boom") }
	defer func() { applyNftablesConfigFn = old }()

	count := func() int {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM lan_acls").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/lan_acls", `{"type":"ip","value":"192.168.20.77","policy":"direct","remark":"x"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
	if count() != before {
		t.Fatal("failed apply left the new ACL row behind")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodDelete, "/api/lan_acls/1"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
	if count() != before {
		t.Fatal("failed apply after delete did not restore the row")
	}
	var value string
	if err := db.QueryRow("SELECT value FROM lan_acls WHERE id=1").Scan(&value); err != nil || value != "192.168.20.10" {
		t.Fatalf("restored row mismatch: %q %v", value, err)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/lan_acls/default_policy", `{"policy":"direct"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
	if got := NewLanACLRepository().GetDefaultPolicy(); got != "proxy" {
		t.Fatalf("default policy not restored: %q", got)
	}
}

func TestDNSSettingsRestoredWhenMosdnsApplyFails(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	oldApply := applyMosdnsConfigFn
	applyMosdnsConfigFn = func() error { return errors.New("mosdns: boom") }
	defer func() { applyMosdnsConfigFn = oldApply }()

	if _, err := db.Exec("INSERT INTO domain_resolve_cache(domain, ips_json, resolved_at, expire_at) VALUES ('remote:keep.example', '[\"1.2.3.4\"]', 1, 9999999999)"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/dns", `{"Local":"114.114.114.114","Remote":"9.9.9.9","Lazy":false,"Mode":"smart"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d", w.Code)
	}
	var local string
	_ = db.QueryRow("SELECT value FROM settings WHERE key='dns_local'").Scan(&local)
	if local != "223.5.5.5" {
		t.Fatalf("dns_local not restored after failed apply: %q", local)
	}

	// Successful apply with unchanged upstreams keeps the resolve cache.
	applyMosdnsConfigFn = func() error { return nil }
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/dns", `{"Local":"223.5.5.5","Remote":"8.8.8.8","Lazy":true,"Mode":"smart","log_level":"warn"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM domain_resolve_cache").Scan(&n)
	if n != 1 {
		t.Fatal("log-level change wiped the resolve cache")
	}
	// Changing an upstream does clear it.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/dns", `{"Local":"223.5.5.5","Remote":"9.9.9.9","Lazy":true,"Mode":"smart"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	_ = db.QueryRow("SELECT COUNT(*) FROM domain_resolve_cache").Scan(&n)
	if n != 0 {
		t.Fatal("upstream change did not clear the resolve cache")
	}
}
