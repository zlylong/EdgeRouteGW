package main

import (
	"testing"
	"time"
)

// waitStaticRouteSyncIdle blocks until the coalescing scheduler has drained, so
// one subtest cannot see the previous one's goroutine.
func waitStaticRouteSyncIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		staticRouteSyncMu.Lock()
		running, pending := staticRouteSyncRunning, staticRouteSyncPending
		staticRouteSyncMu.Unlock()
		if !running && !pending {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("static route sync never went idle")
}

// Publishing is otherwise driven only by domainIPUpdater's five-minute ticker.
// Without a kick here, switching into B or C left the main router holding the
// previous mode's routes — or none — until the next tick happened to fire, and
// nothing surfaced that: the API reported success, every service was healthy,
// and traffic quietly bypassed the gateway. Measured on a live gateway, a
// client fetched 86 KB immediately after switching to C while the gateway's
// outbound counters moved ~1 KB.
func TestModeSwitchKicksRouteSyncForPublishingModes(t *testing.T) {
	setupFeatureSuiteRouter(t)

	oldSync := syncStaticRoutesToOSPFFunc
	t.Cleanup(func() {
		syncStaticRoutesToOSPFFunc = oldSync
		waitStaticRouteSyncIdle(t)
	})

	for _, tc := range []struct {
		mode     string
		wantSync bool
	}{
		{mode: "B", wantSync: true},
		{mode: "C", wantSync: true},
		{mode: "A", wantSync: false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			waitStaticRouteSyncIdle(t)

			// applyModeChange persists the new mode before running the steps,
			// and the scheduler re-reads it from the DB, so mirror that here.
			if _, err := db.Exec("INSERT OR REPLACE INTO settings(key, value) VALUES ('mode', ?)", tc.mode); err != nil {
				t.Fatal(err)
			}

			got := make(chan string, 4)
			syncStaticRoutesToOSPFFunc = func(mode string) { got <- mode }

			if err := modeSwitchFinalizeRoutes(tc.mode); err != nil {
				t.Fatalf("modeSwitchFinalizeRoutes(%s): %v", tc.mode, err)
			}

			select {
			case m := <-got:
				if !tc.wantSync {
					t.Fatalf("mode %s scheduled a sync it should not have", tc.mode)
				}
				if m != tc.mode {
					t.Fatalf("sync ran for mode %q, want %q", m, tc.mode)
				}
			case <-time.After(1 * time.Second):
				if tc.wantSync {
					t.Fatalf("switching to mode %s did not kick a route sync; the main router would wait for the 5-minute ticker", tc.mode)
				}
			}
		})
	}
}

// Mode C's published routes are the resolved addresses of its rules, which a
// switch into C does not invalidate. Demoting them would withdraw working
// routes from the main router only to re-add them moments later.
func TestModeSwitchDemotesPublishedRoutesExceptForModeC(t *testing.T) {
	setupFeatureSuiteRouter(t)

	oldSync := syncStaticRoutesToOSPFFunc
	syncStaticRoutesToOSPFFunc = func(string) {}
	t.Cleanup(func() {
		syncStaticRoutesToOSPFFunc = oldSync
		waitStaticRouteSyncIdle(t)
	})

	seed := func(t *testing.T) {
		t.Helper()
		if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO routes_table(ip, domain, source, status, miss_count) VALUES ('203.0.113.7/32','example.com','static','published',0)"); err != nil {
			t.Fatal(err)
		}
	}
	statusOf := func(t *testing.T) string {
		t.Helper()
		var s string
		if err := db.QueryRow("SELECT status FROM routes_table WHERE ip='203.0.113.7/32'").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("C keeps published", func(t *testing.T) {
		seed(t)
		if err := modeSwitchFinalizeRoutes("C"); err != nil {
			t.Fatal(err)
		}
		if got := statusOf(t); got != "published" {
			t.Fatalf("mode C demoted a published route to %q; the main router would lose it and re-learn it seconds later", got)
		}
	})

	t.Run("A demotes", func(t *testing.T) {
		seed(t)
		if err := modeSwitchFinalizeRoutes("A"); err != nil {
			t.Fatal(err)
		}
		if got := statusOf(t); got != "candidate" {
			t.Fatalf("mode A left the route at %q, want candidate", got)
		}
	})
}
