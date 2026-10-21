// Package resolver turns the domains Mode C must publish into IPv4 addresses
// by asking the local Mosdns (so the answer is exactly what LAN clients get)
// and caches the answers with their real TTL in the store.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Clamp bounds for cached TTLs: too short churns OSPF, too long leaves
// stale CDN addresses published.
const (
	MinTTL = 300 * time.Second
	MaxTTL = time.Hour
	// StaleGrace is how long a failed refresh keeps serving the old answer.
	StaleGrace = 24 * time.Hour
)

// Lookup resolves one name to IPv4 addresses with the minimum answer TTL.
type Lookup func(ctx context.Context, domain string) ([]netip.Addr, time.Duration, error)

// Resolver refreshes the resolve cache.
type Resolver struct {
	Store   *store.Store
	Log     *slog.Logger
	Lookup  Lookup
	Workers int
	// Group tags cache rows; the engine resolves everything through Mosdns
	// today, so there is a single group.
	Group string
}

// New builds a resolver that queries server (host:port) over UDP with TCP
// fallback for truncated answers.
func New(st *store.Store, server string, workers int, log *slog.Logger) *Resolver {
	return &Resolver{Store: st, Log: log, Lookup: DNSLookup(server), Workers: workers, Group: "local"}
}

// DNSLookup returns a Lookup that asks server for A records.
func DNSLookup(server string) Lookup {
	return func(ctx context.Context, domain string) ([]netip.Addr, time.Duration, error) {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(domain), dns.TypeA)
		m.RecursionDesired = true
		c := &dns.Client{Timeout: 4 * time.Second}
		r, _, err := c.ExchangeContext(ctx, m, server)
		if err == nil && r.Truncated {
			c.Net = "tcp"
			r, _, err = c.ExchangeContext(ctx, m, server)
		}
		if err != nil {
			return nil, 0, err
		}
		if r.Rcode != dns.RcodeSuccess {
			return nil, 0, fmt.Errorf("rcode %s", dns.RcodeToString[r.Rcode])
		}
		var addrs []netip.Addr
		minTTL := time.Duration(0)
		for _, rr := range r.Answer {
			a, ok := rr.(*dns.A)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(a.A.To4())
			if !ok {
				continue
			}
			addrs = append(addrs, ip)
			ttl := time.Duration(a.Hdr.Ttl) * time.Second
			if minTTL == 0 || ttl < minTTL {
				minTTL = ttl
			}
		}
		if len(addrs) == 0 {
			return nil, 0, errors.New("no A records")
		}
		return addrs, minTTL, nil
	}
}

// Refresh resolves every domain in want whose cache entry is missing or
// expired, bounded by Workers, and reports how many entries changed. It never
// returns an error for individual lookups; those are recorded on the row.
func (r *Resolver) Refresh(ctx context.Context, want []string) (changed int, err error) {
	if len(want) == 0 {
		return 0, nil
	}
	cached, err := r.Store.ListResolveCache(ctx)
	if err != nil {
		return 0, err
	}
	have := map[string]store.ResolvedEntry{}
	for _, e := range cached {
		if e.Group == r.Group {
			have[e.Domain] = e
		}
	}
	now := time.Now()
	var due []string
	seen := map[string]bool{}
	for _, d := range want {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		if e, ok := have[d]; !ok || e.Expired(now) {
			due = append(due, d)
		}
	}
	if len(due) == 0 {
		return 0, nil
	}
	workers := r.Workers
	if workers <= 0 {
		workers = 8
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range jobs {
				if r.refreshOne(ctx, d, have[d]) {
					mu.Lock()
					changed++
					mu.Unlock()
				}
			}
		}()
	}
	for _, d := range due {
		select {
		case jobs <- d:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return changed, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	return changed, nil
}

// FailRetry is the retry delay after the n-th consecutive failure without
// an answer to keep serving.
func FailRetry(fails int) time.Duration {
	d := 10 * time.Second
	for i := 1; i < fails && d < MinTTL; i++ {
		d *= 2
	}
	if d > MinTTL {
		d = MinTTL
	}
	return d
}

func (r *Resolver) refreshOne(ctx context.Context, domain string, old store.ResolvedEntry) bool {
	now := time.Now()
	addrs, ttl, err := r.Lookup(ctx, domain)
	if err != nil {
		// Keep serving the old answer for a grace period; after that the
		// row expires for real and the routes are withdrawn.
		e := old
		e.Domain, e.Group = domain, r.Group
		e.FailCount++
		e.LastError = err.Error()
		if old.ResolvedAt.IsZero() || now.Sub(old.ResolvedAt) > StaleGrace {
			e.IPs = []string{}
		}
		e.ExpiresAt = now.Add(MinTTL)
		if len(e.IPs) == 0 {
			// Nothing to serve: retry soon with backoff (10s, 20s, ... 5m).
			// A lookup that fails only because Mosdns is restarting must
			// not leave routes unpublished for five minutes.
			e.ExpiresAt = now.Add(FailRetry(e.FailCount))
		}
		if uerr := r.Store.UpsertResolveCache(ctx, e); uerr != nil && r.Log != nil {
			r.Log.Warn("resolve cache write failed", "domain", domain, "err", uerr)
		}
		return len(e.IPs) != len(old.IPs)
	}
	if ttl < MinTTL {
		ttl = MinTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	ips := make([]string, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.String())
	}
	sort.Strings(ips)
	e := store.ResolvedEntry{Domain: domain, Group: r.Group, IPs: ips, TTL: int(ttl.Seconds()), ResolvedAt: now, ExpiresAt: now.Add(ttl)}
	if err := r.Store.UpsertResolveCache(ctx, e); err != nil && r.Log != nil {
		r.Log.Warn("resolve cache write failed", "domain", domain, "err", err)
	}
	return strings.Join(ips, ",") != strings.Join(old.IPs, ",")
}

// NextDue returns when the earliest cached entry among want expires, so the
// caller can sleep exactly that long instead of polling.
func NextDue(entries []store.ResolvedEntry, want map[string]bool, now time.Time) time.Duration {
	next := time.Duration(-1)
	for _, e := range entries {
		if !want[e.Domain] {
			continue
		}
		d := e.ExpiresAt.Sub(now)
		if d < 0 {
			d = 0
		}
		if next < 0 || d < next {
			next = d
		}
	}
	return next
}
