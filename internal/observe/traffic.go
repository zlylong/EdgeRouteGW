package observe

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// EventTraffic carries the current rates every sample interval.
const EventTraffic events.Type = "traffic.sample"

// Rates is the instantaneous view.
type Rates struct {
	At        time.Time       `json:"at"`
	UpBps     float64         `json:"up_bps"`
	DownBps   float64         `json:"down_bps"`
	Nodes     map[string]Rate `json:"nodes"` // outbound tag -> rate
	Available bool            `json:"available"`
}

// Rate is one outbound's rates.
type Rate struct {
	UpBps   float64 `json:"up_bps"`
	DownBps float64 `json:"down_bps"`
}

// StatsSource returns cumulative byte counters per outbound tag.
type StatsSource func(ctx context.Context) (up, down map[string]int64, err error)

// XrayStats reads "xray api statsquery" through the runner.
func XrayStats(run execx.Runner, binary string) StatsSource {
	return func(ctx context.Context) (map[string]int64, map[string]int64, error) {
		res, err := run.Run(ctx, binary, "api", "statsquery", "-s", "127.0.0.1:10085")
		if err != nil {
			return nil, nil, err
		}
		return ParseStats(res.Output)
	}
}

// ParseStats decodes the statsquery JSON ({"stat":[{"name":"outbound>>>tag>>>traffic>>>uplink","value":N},...]}).
func ParseStats(b []byte) (up, down map[string]int64, err error) {
	var doc struct {
		Stat []struct {
			Name  string `json:"name"`
			Value int64  `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}
	up, down = map[string]int64{}, map[string]int64{}
	for _, s := range doc.Stat {
		parts := strings.Split(s.Name, ">>>")
		if len(parts) != 4 || parts[0] != "outbound" || parts[2] != "traffic" {
			continue
		}
		switch parts[3] {
		case "uplink":
			up[parts[1]] = s.Value
		case "downlink":
			down[parts[1]] = s.Value
		}
	}
	return up, down, nil
}

// TrafficSampler polls counters, publishes rates and stores per-minute
// deltas for history.
type TrafficSampler struct {
	Store    *store.Store
	Bus      *events.Bus
	Log      *slog.Logger
	Source   StatsSource
	Interval time.Duration

	mu        sync.RWMutex
	last      Rates
	prevUp    map[string]int64
	prevDown  map[string]int64
	prevAt    time.Time
	accUp     map[string]int64 // bytes accumulated since the last flush
	accDown   map[string]int64
	lastFlush time.Time
}

// NewTrafficSampler wires a sampler with a 10 s interval.
func NewTrafficSampler(st *store.Store, bus *events.Bus, src StatsSource, log *slog.Logger) *TrafficSampler {
	return &TrafficSampler{Store: st, Bus: bus, Log: log, Source: src, Interval: 10 * time.Second, accUp: map[string]int64{}, accDown: map[string]int64{}}
}

// Current returns the latest rates.
func (t *TrafficSampler) Current() Rates {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.last
}

// Run samples until ctx ends.
func (t *TrafficSampler) Run(ctx context.Context) error {
	tick := time.NewTicker(t.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.flush(ctx, true)
			return nil
		case <-tick.C:
			t.sample(ctx)
		}
	}
}

func (t *TrafficSampler) sample(ctx context.Context) {
	now := time.Now()
	up, down, err := t.Source(ctx)
	if err != nil {
		t.mu.Lock()
		t.last = Rates{At: now, Nodes: map[string]Rate{}}
		t.prevUp, t.prevDown = nil, nil
		t.mu.Unlock()
		return
	}
	t.mu.Lock()
	r := Rates{At: now, Nodes: map[string]Rate{}, Available: true}
	if t.prevUp != nil {
		dt := now.Sub(t.prevAt).Seconds()
		if dt > 0 {
			for tag, v := range up {
				d := v - t.prevUp[tag]
				if d < 0 { // counter reset (restart)
					d = 0
				}
				t.accUp[tag] += d
				nr := r.Nodes[tag]
				nr.UpBps = float64(d) / dt
				r.Nodes[tag] = nr
				if tag != "direct" && tag != "block" {
					r.UpBps += nr.UpBps
				}
			}
			for tag, v := range down {
				d := v - t.prevDown[tag]
				if d < 0 {
					d = 0
				}
				t.accDown[tag] += d
				nr := r.Nodes[tag]
				nr.DownBps = float64(d) / dt
				r.Nodes[tag] = nr
				if tag != "direct" && tag != "block" {
					r.DownBps += nr.DownBps
				}
			}
		}
	}
	t.prevUp, t.prevDown, t.prevAt = up, down, now
	t.last = r
	t.mu.Unlock()
	if t.Bus != nil {
		t.Bus.Publish(EventTraffic, r)
	}
	t.flush(ctx, false)
}

// flush writes accumulated deltas once per minute.
func (t *TrafficSampler) flush(ctx context.Context, force bool) {
	t.mu.Lock()
	if !force && time.Since(t.lastFlush) < time.Minute {
		t.mu.Unlock()
		return
	}
	accUp, accDown := t.accUp, t.accDown
	t.accUp, t.accDown = map[string]int64{}, map[string]int64{}
	t.lastFlush = time.Now()
	t.mu.Unlock()
	var totalUp, totalDown int64
	per := map[string][2]int64{}
	for tag, v := range accUp {
		if tag == "direct" || tag == "block" {
			continue
		}
		totalUp += v
		e := per[tag]
		e[0] += v
		per[tag] = e
	}
	for tag, v := range accDown {
		if tag == "direct" || tag == "block" {
			continue
		}
		totalDown += v
		e := per[tag]
		e[1] += v
		per[tag] = e
	}
	if totalUp == 0 && totalDown == 0 {
		return
	}
	ts := time.Now().Truncate(time.Minute).Unix()
	if err := t.Store.AddTrafficSample(ctx, ts, totalUp, totalDown, per); err != nil && t.Log != nil {
		t.Log.Warn("traffic sample write failed", "err", err)
	}
}
