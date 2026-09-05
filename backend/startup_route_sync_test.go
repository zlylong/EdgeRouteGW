package main

import (
	"testing"
	"time"
)

// After a restart the first sync is kicked while Xray and Mosdns are still
// starting, resolves nothing, and — before this — nothing tried again until
// the five-minute ticker. Both update.sh and flush_cache.sh wipe routes_table
// and restart, so every upgrade in Mode B or C left the main router without
// routes for that long. The burst has to fire for both publishing modes and
// stay quiet for Mode A.
func TestStartupRouteSyncBurstFiresForPublishingModes(t *testing.T) {
	setupFeatureSuiteRouter(t)

	oldBackoff := startupRouteSyncBackoff
	oldSync := syncStaticRoutesToOSPFFunc
	startupRouteSyncBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() {
		startupRouteSyncBackoff = oldBackoff
		syncStaticRoutesToOSPFFunc = oldSync
		waitStaticRouteSyncIdle(t)
	})

	for _, tc := range []struct {
		mode     string
		wantSync bool
	}{
		{"C", true},
		{"B", true},
		{"A", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			waitStaticRouteSyncIdle(t)
			if _, err := db.Exec("INSERT OR REPLACE INTO settings(key, value) VALUES ('mode', ?)", tc.mode); err != nil {
				t.Fatal(err)
			}
			got := make(chan string, 8)
			syncStaticRoutesToOSPFFunc = func(mode string) { got <- mode }

			startupRouteSyncBurst()

			select {
			case m := <-got:
				if !tc.wantSync {
					t.Fatalf("mode %s: startup burst scheduled a sync it should not have", tc.mode)
				}
				if m != tc.mode {
					t.Fatalf("sync ran for %q, want %q", m, tc.mode)
				}
			case <-time.After(500 * time.Millisecond):
				if tc.wantSync {
					t.Fatalf("mode %s: startup burst never scheduled a sync; the main router would wait for the ticker", tc.mode)
				}
			}
		})
	}
}

// The production backoff has to actually be short: the whole point is to
// beat the five-minute ticker by a wide margin.
func TestStartupRouteSyncBackoffConvergesWellBeforeTheTicker(t *testing.T) {
	var total time.Duration
	for _, d := range startupRouteSyncBackoff {
		total += d
	}
	if len(startupRouteSyncBackoff) < 2 {
		t.Fatalf("burst has %d step(s); one attempt cannot cover a service that is still starting", len(startupRouteSyncBackoff))
	}
	if startupRouteSyncBackoff[0] > 30*time.Second {
		t.Fatalf("first retry at %v is too late to be a startup burst", startupRouteSyncBackoff[0])
	}
	if total >= 5*time.Minute {
		t.Fatalf("burst spans %v, which is no better than the ticker it exists to beat", total)
	}
}
