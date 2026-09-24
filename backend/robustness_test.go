package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ringContains(target string) bool {
	for _, r := range GetRecentConnections() {
		if r.Target == target {
			return true
		}
	}
	return false
}

func TestTailerSurvivesTruncation(t *testing.T) {
	setupFeatureSuiteRouter(t)
	oldPoll := tailerPollInterval
	tailerPollInterval = 20 * time.Millisecond
	defer func() { tailerPollInterval = oldPoll }()

	path := filepath.Join(t.TempDir(), "access.log")
	line := func(port string) string {
		return "2026/09/24 10:00:00 from 192.168.20.5:5000 accepted tcp:example.com:" + port + " [proxy-1-out]\n"
	}
	if err := os.WriteFile(path, []byte(line("1001")), 0o644); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { tailAccessLog(path, stop); close(done) }()
	defer func() { close(stop); <-done }()

	waitFor := func(port string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if ringContains("example.com:" + port) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("line for port %s never reached the ring", port)
	}
	waitFor("1001")

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(line("1002"))
	_ = f.Close()
	waitFor("1002")

	// Truncate (as the cleanup goroutine does) and write a shorter file: the
	// old tailer kept its offset and skipped everything until the file grew
	// past it again.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(line("1003"))
	_ = f.Close()
	waitFor("1003")

	// Rotation: replace the file with a new inode.
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte(line("1004")), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor("1004")
}

func TestGoSafeLoopRestartsAfterPanic(t *testing.T) {
	var runs int32
	done := make(chan struct{})
	goSafeLoop("test loop", func() {
		n := atomic.AddInt32(&runs, 1)
		if n == 1 {
			panic("first run explodes")
		}
		close(done)
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop was not restarted after the panic")
	}
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("runs = %d want 2", got)
	}
}

func TestParseDigAnswerLinesReturnsMinTTL(t *testing.T) {
	out := `
; <<>> DiG 9.18 <<>> +noall +answer @127.0.0.1 example.com A
www.example.com.	120	IN	CNAME	example.com.
example.com.		299	IN	A	93.184.216.34
example.com.		30	IN	A	93.184.216.35
example.com.		299	IN	AAAA	2606:2800:220:1:248:1893:25c8:1946
`
	ips, ttl, err := parseDigAnswerLines(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 2 || ttl != 30 {
		t.Fatalf("ips=%v ttl=%d, want 2 A records and min TTL 30", ips, ttl)
	}
	if _, _, err := parseDigAnswerLines("example.com. 300 IN CNAME other.example.\n"); err == nil {
		t.Fatal("CNAME-only answer must be an error")
	}
}

func TestResolverUsesAnswerTTLInsteadOfFloor(t *testing.T) {
	old := runDig
	defer func() { runDig = old }()
	var gotArgs []string
	runDig = func(ctx context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("cdn.example.\t1800\tIN\tA\t203.0.113.9\n"), nil
	}
	ips, ttl, err := resolveDomainIPv4WithTTL("cdn.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || ttl != 1800 {
		t.Fatalf("ips=%v ttl=%d, want the 1800s answer TTL (was always 300)", ips, ttl)
	}
	joined := strings.Join(gotArgs, " ")
	if strings.Contains(joined, "+short") || !strings.Contains(joined, "+noall +answer") {
		t.Fatalf("dig invoked with %q; +short discards TTLs", joined)
	}
	runDig = func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, errors.New("connection timed out")
	}
	if _, _, err := resolveDomainIPv4WithTTL("cdn.example"); err == nil {
		t.Fatal("dig failure must propagate")
	}
}

func TestBatchDomainLookupPrefersRoutesTableThenResolveCache(t *testing.T) {
	setupFeatureSuiteRouter(t)
	invalidateResolveCacheIndex()
	if _, err := db.Exec("INSERT INTO domain_resolve_cache(domain, ips_json, resolved_at, expire_at) VALUES ('remote:cached.example', '[\"203.0.113.7\",\"203.0.113.8\"]', 100, 9999999999)"); err != nil {
		t.Fatal(err)
	}
	got := batchLookupRecentDomains([]string{"8.8.8.8", "203.0.113.8", "198.51.100.1"})
	if got["8.8.8.8"] != "google.com" {
		t.Fatalf("routes_table /32 lookup failed: %v", got)
	}
	if got["203.0.113.8"] != "cached.example" {
		t.Fatalf("resolve cache reverse index failed: %v", got)
	}
	if got["198.51.100.1"] != "" {
		t.Fatalf("unknown ip resolved to %q", got["198.51.100.1"])
	}
}

func BenchmarkAttachRuleMatchMeta(b *testing.B) {
	d, err := openSQLite(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	old := getDB()
	setDB(d)
	defer setDB(old)
	for _, stmt := range []string{
		`CREATE TABLE rules (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, value TEXT, policy TEXT, priority INTEGER NOT NULL DEFAULT 0, group_id TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE routes_table (ip TEXT PRIMARY KEY, domain TEXT, source TEXT, first_seen DATETIME, last_seen DATETIME, ttl INTEGER, status TEXT, miss_count INTEGER DEFAULT 0)`,
		`CREATE TABLE domain_resolve_cache (domain TEXT PRIMARY KEY, ips_json TEXT NOT NULL, dns_ttl INTEGER NOT NULL DEFAULT 300, resolved_at DATETIME NOT NULL, expire_at DATETIME NOT NULL, last_error TEXT NOT NULL DEFAULT '', fail_count INTEGER NOT NULL DEFAULT 0, geodata_ver TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := d.Exec(stmt); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		_, _ = d.Exec("INSERT INTO rules(type, value, policy) VALUES ('domain', ?, 'proxy')", "site"+itoa(i)+".example")
		_, _ = d.Exec("INSERT INTO domain_resolve_cache(domain, ips_json, resolved_at, expire_at) VALUES (?, ?, ?, 9999999999)", "remote:site"+itoa(i)+".example", `["10.1.`+itoa(i)+`.1"]`, i)
	}
	records := make([]ConnectionRecord, 200)
	for i := range records {
		records[i] = ConnectionRecord{Target: "10.1." + itoa(i%50) + ".1:443", Policy: "proxy-1-out"}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		attachRuleMatchMeta(append([]ConnectionRecord(nil), records...))
	}
}
