package observe

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// EventLatency is published after a probe round with the per-node results.
const EventLatency events.Type = "nodes.latency"

// LatencyResult is one node's probe outcome; Ms is -1 when unreachable.
type LatencyResult struct {
	NodeID    string    `json:"node_id"`
	Ms        int       `json:"ms"`
	Error     string    `json:"error,omitempty"`
	Via       string    `json:"via"` // "xray": HTTP through the node's outbound; "tcp": connect to address:port
	CheckedAt time.Time `json:"checked_at"`
}

// ProbeURLKey is the settings key overriding the probe target.
const ProbeURLKey = "probe.url"

// DefaultProbeURL answers 204 quickly from anywhere; it is fetched through
// the node, so a reply proves the whole tunnel works.
const DefaultProbeURL = "http://cp.cloudflare.com/generate_204"

// LatencyProber checks every enabled node. With ViaXray it sends an HTTP
// request through the Xray probe inbound (account probe-<slot> routes to
// that node), which works for every protocol including WireGuard and
// measures the real round trip; otherwise it falls back to a TCP connect
// to the node's address:port, a reachability signal only.
type LatencyProber struct {
	Store    *store.Store
	Bus      *events.Bus
	Log      *slog.Logger
	Interval time.Duration
	Timeout  time.Duration
	Dial     func(ctx context.Context, addr string) (time.Duration, error)
	// Probe performs the end-to-end check for one slot account; nil when
	// Xray is not managed (development).
	Probe     func(ctx context.Context, user, pass, url string) (time.Duration, error)
	ProxyAddr string
	URL       string

	mu      sync.Mutex
	running bool
	kick    chan struct{}
	last    map[string]LatencyResult
}

// NewLatencyProber wires the production prober (TCP only until EnableXray).
func NewLatencyProber(st *store.Store, bus *events.Bus, log *slog.Logger) *LatencyProber {
	p := &LatencyProber{Store: st, Bus: bus, Log: log, Interval: 5 * time.Minute, Timeout: 8 * time.Second, kick: make(chan struct{}, 1), last: map[string]LatencyResult{}, URL: DefaultProbeURL}
	p.Dial = p.tcpDial
	return p
}

// EnableXray switches to end-to-end probing through the probe inbound.
func (p *LatencyProber) EnableXray(proxyAddr string) {
	p.ProxyAddr = proxyAddr
	p.Probe = p.httpViaSocks
}

func (p *LatencyProber) httpViaSocks(ctx context.Context, user, pass, url string) (time.Duration, error) {
	dialer, err := proxy.SOCKS5("tcp", p.ProxyAddr, &proxy.Auth{User: user, Password: pass}, &net.Dialer{Timeout: p.Timeout})
	if err != nil {
		return 0, err
	}
	cd, _ := dialer.(proxy.ContextDialer)
	tr := &http.Transport{DialContext: cd.DialContext, DisableKeepAlives: true, ResponseHeaderTimeout: p.Timeout}
	client := &http.Client{Transport: tr, Timeout: p.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "EdgeRouteGW-probe/2.0")
	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	if res.StatusCode >= 400 {
		return 0, fmt.Errorf("probe target answered %d", res.StatusCode)
	}
	return time.Since(start), nil
}

// Results returns the latest outcome per node.
func (p *LatencyProber) Results() map[string]LatencyResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]LatencyResult, len(p.last))
	for k, v := range p.last {
		out[k] = v
	}
	return out
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

	rows, err := p.Store.ListNodes(ctx)
	if err != nil {
		return nil
	}
	nodes := make([]model.Node, 0, len(rows))
	for _, r := range rows {
		nodes = append(nodes, r.Node)
	}
	slots := render.ProbeSlots(nodes)
	url := p.URL
	if v, err := p.Store.GetSetting(ctx, ProbeURLKey); err == nil && strings.TrimSpace(v) != "" {
		url = strings.TrimSpace(v)
	}
	results := make([]LatencyResult, 0, len(nodes))
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		slot, hasSlot := slots[n.ID]
		wg.Add(1)
		go func(id, addr string, slot int, hasSlot bool) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := LatencyResult{NodeID: id, Ms: -1, Via: "tcp", CheckedAt: time.Now()}
			var d time.Duration
			var err error
			if p.Probe != nil && hasSlot {
				r.Via = "xray"
				d, err = p.Probe(ctx, render.ProbeUser(slot), render.ProbePassword, url)
			} else {
				d, err = p.Dial(ctx, addr)
			}
			if err != nil {
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
			p.last[id] = r
			mu.Unlock()
		}(n.ID, net.JoinHostPort(n.Address, strconv.Itoa(n.Port)), slot, hasSlot)
	}
	wg.Wait()
	// Forget nodes that no longer exist.
	mu.Lock()
	for id := range p.last {
		found := false
		for _, n := range nodes {
			if n.ID == id {
				found = true
				break
			}
		}
		if !found {
			delete(p.last, id)
		}
	}
	mu.Unlock()
	if p.Bus != nil {
		p.Bus.Publish(EventLatency, results)
	}
	return results
}
