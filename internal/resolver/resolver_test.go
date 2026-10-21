package resolver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRefreshCachesAndClampsTTL(t *testing.T) {
	st := newStore(t)
	var calls atomic.Int32
	r := &Resolver{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Workers: 2, Group: "local",
		Lookup: func(_ context.Context, d string) ([]netip.Addr, time.Duration, error) {
			calls.Add(1)
			switch d {
			case "a.com":
				return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("1.0.0.1")}, 30 * time.Second, nil
			case "b.com":
				return []netip.Addr{netip.MustParseAddr("2.2.2.2")}, 48 * time.Hour, nil
			default:
				return nil, 0, errors.New("NXDOMAIN")
			}
		}}
	ctx := context.Background()
	changed, err := r.Refresh(ctx, []string{"a.com", "A.com.", "b.com", "nx.com"})
	if err != nil {
		t.Fatal(err)
	}
	if changed != 2 || calls.Load() != 3 {
		t.Fatalf("changed=%d calls=%d", changed, calls.Load())
	}
	entries, _ := st.ListResolveCache(ctx)
	byDomain := map[string]store.ResolvedEntry{}
	for _, e := range entries {
		byDomain[e.Domain] = e
	}
	if byDomain["a.com"].TTL != 300 || byDomain["b.com"].TTL != 3600 {
		t.Fatalf("ttl clamp: a=%d b=%d", byDomain["a.com"].TTL, byDomain["b.com"].TTL)
	}
	if len(byDomain["a.com"].IPs) != 2 || byDomain["a.com"].IPs[0] != "1.0.0.1" {
		t.Fatalf("a.com ips %v", byDomain["a.com"].IPs)
	}
	if byDomain["nx.com"].FailCount != 1 || byDomain["nx.com"].LastError == "" || len(byDomain["nx.com"].IPs) != 0 {
		t.Fatalf("failure row %+v", byDomain["nx.com"])
	}
	// Fresh entries are not re-queried.
	calls.Store(0)
	if _, err := r.Refresh(ctx, []string{"a.com", "b.com"}); err != nil || calls.Load() != 0 {
		t.Fatalf("fresh entries re-queried: %d", calls.Load())
	}
}

func TestFailureKeepsStaleAnswer(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	old := store.ResolvedEntry{Domain: "c.com", Group: "local", IPs: []string{"3.3.3.3"}, TTL: 300, ResolvedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute)}
	_ = st.UpsertResolveCache(ctx, old)
	r := &Resolver{Store: st, Workers: 1, Group: "local", Lookup: func(context.Context, string) ([]netip.Addr, time.Duration, error) {
		return nil, 0, errors.New("timeout")
	}}
	changed, _ := r.Refresh(ctx, []string{"c.com"})
	entries, _ := st.ListResolveCache(ctx)
	if changed != 0 || len(entries) != 1 || len(entries[0].IPs) != 1 || entries[0].FailCount != 1 || entries[0].ExpiresAt.Before(time.Now()) {
		t.Fatalf("stale handling: changed=%d %+v", changed, entries)
	}
}

func TestFailRetryBackoff(t *testing.T) {
	want := []time.Duration{10 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, MinTTL, MinTTL}
	for i, w := range want {
		if got := FailRetry(i); got != w {
			t.Errorf("fails=%d got %v want %v", i, got, w)
		}
	}
}
