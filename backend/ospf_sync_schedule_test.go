package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduleStaticRouteSyncRunsAsyncAndCoalescesPending(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("INSERT OR REPLACE INTO settings(key, value) VALUES ('mode', 'C')"); err != nil {
		t.Fatal(err)
	}

	oldSync := syncStaticRoutesToOSPFFunc
	defer func() { syncStaticRoutesToOSPFFunc = oldSync }()

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	syncStaticRoutesToOSPFFunc = func(mode string) {
		calls.Add(1)
		started <- struct{}{}
		<-release
	}

	begin := time.Now()
	scheduleStaticRouteSync("C")
	if time.Since(begin) > 200*time.Millisecond {
		t.Fatalf("scheduleStaticRouteSync blocked too long: %v", time.Since(begin))
	}

	select {
	case <-started:
	case <-time.After(1 * time.Second):
		t.Fatal("first async sync did not start")
	}

	scheduleStaticRouteSync("C")
	release <- struct{}{}

	select {
	case <-started:
	case <-time.After(1 * time.Second):
		t.Fatal("pending async sync did not rerun")
	}

	release <- struct{}{}
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		staticRouteSyncMu.Lock()
		running := staticRouteSyncRunning
		pending := staticRouteSyncPending
		staticRouteSyncMu.Unlock()
		if !running && !pending {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if calls.Load() != 2 {
		t.Fatalf("unexpected sync call count: %d", calls.Load())
	}
}
