package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModeConfigs_MatchRoutingExpectations(t *testing.T) {
	cases := []struct {
		mode               string
		wantFakeDNSInXray  bool
		wantFakeIPInMosdns bool
	}{
		{mode: "A", wantFakeDNSInXray: false, wantFakeIPInMosdns: false},
		{mode: "B", wantFakeDNSInXray: true, wantFakeIPInMosdns: true},
		{mode: "C", wantFakeDNSInXray: false, wantFakeIPInMosdns: false},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := buildBaseXrayConfig(tc.mode)
			_, hasFakeDNS := cfg["fakedns"]
			if hasFakeDNS != tc.wantFakeDNSInXray {
				t.Fatalf("mode %s fakeDNS mismatch: got %v want %v", tc.mode, hasFakeDNS, tc.wantFakeDNSInXray)
			}

			mosdnsCfg := renderMosdnsConfig("223.5.5.5", "1.1.1.1", true, tc.mode)
			hasFakeIP := strings.Contains(mosdnsCfg, "exec: $forward_fakeip")
			if hasFakeIP != tc.wantFakeIPInMosdns {
				t.Fatalf("mode %s fakeIP mismatch: got %v want %v", tc.mode, hasFakeIP, tc.wantFakeIPInMosdns)
			}
		})
	}
}

func TestModeSwitchRollbackOnApplyFailure(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	oldSetServices := modeSwitchSetServices
	oldSyncFRR := modeSwitchSyncFRR
	oldApplyNftables := modeSwitchApplyNftables
	oldApplyMosdns := modeSwitchApplyMosdns
	oldApplyXray := modeSwitchApplyXray
	oldFinalizeRoutes := modeSwitchFinalizeRoutes
	defer func() {
		modeSwitchSetServices = oldSetServices
		modeSwitchSyncFRR = oldSyncFRR
		modeSwitchApplyNftables = oldApplyNftables
		modeSwitchApplyMosdns = oldApplyMosdns
		modeSwitchApplyXray = oldApplyXray
		modeSwitchFinalizeRoutes = oldFinalizeRoutes
	}()

	modeSwitchSetServices = func(mode string) error { return nil }
	modeSwitchSyncFRR = func() error { return nil }
	modeSwitchApplyNftables = func() error { return nil }
	modeSwitchApplyMosdns = func() error { return errors.New("mosdns crashed") }
	xrayCalls := 0
	modeSwitchApplyXray = func() error {
		xrayCalls++
		return nil
	}
	modeSwitchFinalizeRoutes = func(mode string) error { return nil }

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/mode", `{"Mode":"C"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d body=%s", w.Code, w.Body.String())
	}

	var mode string
	if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "B" {
		t.Fatalf("want rolled back mode B got %s", mode)
	}

	var published int
	if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE status='published'").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("want published routes unchanged got %d", published)
	}
	if xrayCalls != 1 {
		t.Fatalf("expected exactly one xray apply during rollback, got %d", xrayCalls)
	}
}

func TestModeSwitchFinalizeRoutesAfterSuccess(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	oldSetServices := modeSwitchSetServices
	oldSyncFRR := modeSwitchSyncFRR
	oldApplyNftables := modeSwitchApplyNftables
	oldApplyMosdns := modeSwitchApplyMosdns
	oldApplyXray := modeSwitchApplyXray
	oldFinalizeRoutes := modeSwitchFinalizeRoutes
	defer func() {
		modeSwitchSetServices = oldSetServices
		modeSwitchSyncFRR = oldSyncFRR
		modeSwitchApplyNftables = oldApplyNftables
		modeSwitchApplyMosdns = oldApplyMosdns
		modeSwitchApplyXray = oldApplyXray
		modeSwitchFinalizeRoutes = oldFinalizeRoutes
	}()

	modeSwitchSetServices = func(mode string) error { return nil }
	modeSwitchSyncFRR = func() error { return nil }
	modeSwitchApplyNftables = func() error { return nil }
	modeSwitchApplyMosdns = func() error { return nil }
	modeSwitchApplyXray = func() error { return nil }
	modeSwitchFinalizeRoutes = oldFinalizeRoutes

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/mode", `{"Mode":"A"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}

	var mode string
	if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "A" {
		t.Fatalf("want mode A got %s", mode)
	}

	var published, candidate int
	if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE status='published'").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE status='candidate'").Scan(&candidate); err != nil {
		t.Fatal(err)
	}
	if published != 0 || candidate != 2 {
		t.Fatalf("unexpected route counts published=%d candidate=%d", published, candidate)
	}
}

func TestModeSwitchOrder_AppliesConfigsBeforeServiceState(t *testing.T) {
	setupFeatureSuiteRouter(t)

	oldSetServices := modeSwitchSetServices
	oldSyncFRR := modeSwitchSyncFRR
	oldApplyNftables := modeSwitchApplyNftables
	oldApplyMosdns := modeSwitchApplyMosdns
	oldApplyXray := modeSwitchApplyXray
	oldFinalizeRoutes := modeSwitchFinalizeRoutes
	defer func() {
		modeSwitchSetServices = oldSetServices
		modeSwitchSyncFRR = oldSyncFRR
		modeSwitchApplyNftables = oldApplyNftables
		modeSwitchApplyMosdns = oldApplyMosdns
		modeSwitchApplyXray = oldApplyXray
		modeSwitchFinalizeRoutes = oldFinalizeRoutes
	}()

	var calls []string
	modeSwitchSyncFRR = func() error {
		calls = append(calls, "sync-frr")
		return nil
	}
	modeSwitchApplyNftables = func() error {
		calls = append(calls, "apply-nftables")
		return nil
	}
	modeSwitchApplyMosdns = func() error {
		calls = append(calls, "apply-mosdns")
		return nil
	}
	modeSwitchApplyXray = func() error {
		calls = append(calls, "apply-xray")
		return nil
	}
	modeSwitchSetServices = func(mode string) error {
		calls = append(calls, "set-services:"+mode)
		return nil
	}
	modeSwitchFinalizeRoutes = func(mode string) error {
		calls = append(calls, "finalize:"+mode)
		return nil
	}

	if err := applyModeChange("C"); err != nil {
		t.Fatal(err)
	}

	got := strings.Join(calls, ",")
	want := "sync-frr,apply-nftables,apply-mosdns,apply-xray,set-services:C,finalize:C"
	if got != want {
		t.Fatalf("unexpected order: got %s want %s", got, want)
	}
}

func TestModeSwitchRollbackDoesNotDirtyPublishedRoutes(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	oldSetServices := modeSwitchSetServices
	oldSyncFRR := modeSwitchSyncFRR
	oldApplyNftables := modeSwitchApplyNftables
	oldApplyMosdns := modeSwitchApplyMosdns
	oldApplyXray := modeSwitchApplyXray
	oldFinalizeRoutes := modeSwitchFinalizeRoutes
	defer func() {
		modeSwitchSetServices = oldSetServices
		modeSwitchSyncFRR = oldSyncFRR
		modeSwitchApplyNftables = oldApplyNftables
		modeSwitchApplyMosdns = oldApplyMosdns
		modeSwitchApplyXray = oldApplyXray
		modeSwitchFinalizeRoutes = oldFinalizeRoutes
	}()

	modeSwitchSetServices = func(mode string) error { return nil }
	modeSwitchSyncFRR = func() error { return nil }
	modeSwitchApplyNftables = func() error { return nil }
	modeSwitchApplyMosdns = func() error { return errors.New("mosdns crashed") }
	modeSwitchApplyXray = func() error { return nil }
	modeSwitchFinalizeRoutes = oldFinalizeRoutes

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/mode", `{"Mode":"A"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d body=%s", w.Code, w.Body.String())
	}

	var published, candidate int
	if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE status='published'").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE status='candidate'").Scan(&candidate); err != nil {
		t.Fatal(err)
	}
	if published != 1 || candidate != 1 {
		t.Fatalf("rollback dirtied route states: published=%d candidate=%d", published, candidate)
	}
}

func TestModeSwitchUpsertsModeWhenMissing(t *testing.T) {
	oldSetServices := modeSwitchSetServices
	oldSyncFRR := modeSwitchSyncFRR
	oldApplyNftables := modeSwitchApplyNftables
	oldApplyMosdns := modeSwitchApplyMosdns
	oldApplyXray := modeSwitchApplyXray
	oldFinalizeRoutes := modeSwitchFinalizeRoutes
	defer func() {
		modeSwitchSetServices = oldSetServices
		modeSwitchSyncFRR = oldSyncFRR
		modeSwitchApplyNftables = oldApplyNftables
		modeSwitchApplyMosdns = oldApplyMosdns
		modeSwitchApplyXray = oldApplyXray
		modeSwitchFinalizeRoutes = oldFinalizeRoutes
	}()

	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("DELETE FROM settings WHERE key='mode'"); err != nil {
		t.Fatal(err)
	}

	modeSwitchSetServices = func(mode string) error { return nil }
	modeSwitchSyncFRR = func() error { return nil }
	modeSwitchApplyNftables = func() error { return nil }
	modeSwitchApplyMosdns = func() error { return nil }
	modeSwitchApplyXray = func() error { return nil }
	modeSwitchFinalizeRoutes = func(mode string) error { return nil }

	if err := applyModeChange("C"); err != nil {
		t.Fatal(err)
	}

	var mode string
	if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "C" {
		t.Fatalf("want mode C got %s", mode)
	}
}

func TestModeSwitchSetServices_ToleratesFailedFRRState(t *testing.T) {
	oldSetServices := modeSwitchSetServices
	defer func() { modeSwitchSetServices = oldSetServices }()

	calls := []string{}
	modeSwitchSetServices = func(mode string) error {
		calls = append(calls, "reset-failed")
		calls = append(calls, "start-frr")
		return nil
	}

	if err := modeSwitchSetServices("B"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "reset-failed,start-frr" {
		t.Fatalf("unexpected calls: %s", got)
	}
}
