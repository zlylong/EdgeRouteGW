package resolver

import (
	"context"
	"log/slog"
	"time"
)

// Loop keeps the cache fresh for the domains a provider names and wakes the
// reconciler when answers change. It sleeps until the earliest cached entry
// expires (bounded by MinInterval/MaxInterval) and re-reads the domain list
// each round, so rule edits are picked up without a separate signal.
type Loop struct {
	Resolver    *Resolver
	Domains     func(ctx context.Context) ([]string, error)
	Notify      func()
	Log         *slog.Logger
	MinInterval time.Duration
	MaxInterval time.Duration
	// Kick wakes the loop early (a write that may add domains).
	kick chan struct{}
}

// NewLoop wires a loop with production timings.
func NewLoop(r *Resolver, domains func(ctx context.Context) ([]string, error), notify func(), log *slog.Logger) *Loop {
	return &Loop{Resolver: r, Domains: domains, Notify: notify, Log: log, MinInterval: 10 * time.Second, MaxInterval: 5 * time.Minute, kick: make(chan struct{}, 1)}
}

// Kick asks the loop to run a round soon.
func (l *Loop) Kick() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// Run executes until ctx ends.
func (l *Loop) Run(ctx context.Context) error {
	for {
		wait := l.round(ctx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		case <-l.kick:
			timer.Stop()
			// Debounce bursts of writes.
			t := time.NewTimer(500 * time.Millisecond)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return nil
			}
		}
	}
}

// round refreshes once and returns how long to wait before the next one.
func (l *Loop) round(ctx context.Context) time.Duration {
	domains, err := l.Domains(ctx)
	if err != nil {
		l.Log.Warn("resolver: cannot list domains", "err", err)
		return l.MinInterval
	}
	if len(domains) == 0 {
		return l.MaxInterval
	}
	changed, err := l.Resolver.Refresh(ctx, domains)
	if err != nil {
		l.Log.Warn("resolver: refresh failed", "err", err)
		return l.MinInterval
	}
	if changed > 0 {
		l.Log.Info("resolver: answers changed", "domains", changed)
		if l.Notify != nil {
			l.Notify()
		}
	}
	entries, err := l.Resolver.Store.ListResolveCache(ctx)
	if err != nil {
		return l.MinInterval
	}
	want := map[string]bool{}
	for _, d := range domains {
		want[d] = true
	}
	next := NextDue(entries, want, time.Now())
	if next < 0 {
		next = l.MaxInterval
	}
	if next < l.MinInterval {
		next = l.MinInterval
	}
	if next > l.MaxInterval {
		next = l.MaxInterval
	}
	return next
}
