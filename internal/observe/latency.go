package observe

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// EventLatency is published after a probe round with the per-node results.
const EventLatency events.Type = "nodes.latency"

// LatencyResult is one node's probe outcome; Ms is -1 when unreachable.
type LatencyResult struct {
	NodeID string `json:"node_id"`
	Ms     int    `json:"ms"`
	Error  string `json:"error,omitempty"`
}

// LatencyProber measures TCP connect time to each node's address:port. It is
// a reachability signal, not a throughput test: a node that does not answer
// a SYN within the timeout is marked unreachable.
type LatencyProber struct {
	Store    *store.Store
	Bus      *events.Bus
	Log      *slog.Logger
	Interval time.Duration
	Timeout  time.Duration
	Dial     func(ctx context.Context, addr string) (time.Duration, error)

	mu      sync.Mutex
	running bool
	kick    chan struct{}
}

// NewLatencyProber wires the production prober.
func NewLatencyProber(st *store.Store, bus *events.Bus, log *slog.Logger) *LatencyProber {
	p := &LatencyProber{Store: st, Bus: bus, Log: log, Interval: 5 * time.Minute, Timeout: 5 * time.Second, kick: make(chan struct{}, 1)}
	p.Dial = p.tcpDial
	return p
}

func (p *LatencyProber) tcpDial(ctx context.Context, addr string) (time.Duration, error) {
	d := net.Dialer{Timeout: p.Timeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, err
	}
	c.Close()
	return time.Since(start), nil
}

// Kick requests a probe round now (the API's "测速" button).
func (p *LatencyProber) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run probes periodically until ctx ends.
func (p *LatencyProber) Run(ctx context.Context) error {
	t := time.NewTimer(10 * time.Second) // first round shortly after start
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-p.kick:
		}
		p.Round(ctx)
		t.Reset(p.Interval)
	}
}

// Round probes every enabled node once and stores the results.
func (p *LatencyProber) Round(ctx context.Context) []LatencyResult {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.running = false; p.mu.Unlock() }()

	nodes, err := p.Store.ListNodes(ctx)
	if err != nil {
		return nil
	}
	results := make([]LatencyResult, 0, len(nodes))
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		wg.Add(1)
		go func(id, addr string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := LatencyResult{NodeID: id, Ms: -1}
			if d, err := p.Dial(ctx, addr); err != nil {
				r.Error = err.Error()
			} else {
				r.Ms = int(d.Milliseconds())
				if r.Ms == 0 {
					r.Ms = 1
				}
			}
			ms := r.Ms
			_ = p.Store.UpdateNodeLatency(ctx, id, &ms)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(n.ID, net.JoinHostPort(n.Address, strconv.Itoa(n.Port)))
	}
	wg.Wait()
	if p.Bus != nil {
		p.Bus.Publish(EventLatency, results)
	}
	return results
}
