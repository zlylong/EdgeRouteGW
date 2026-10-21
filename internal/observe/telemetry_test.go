package observe

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func TestParseStats(t *testing.T) {
	up, down, err := ParseStats([]byte(`{"stat":[{"name":"outbound>>>node-a>>>traffic>>>uplink","value":100},{"name":"outbound>>>node-a>>>traffic>>>downlink","value":2000},{"name":"inbound>>>tproxy_in>>>traffic>>>uplink","value":5},{"name":"outbound>>>direct>>>traffic>>>downlink","value":7}]}`))
	if err != nil || up["node-a"] != 100 || down["node-a"] != 2000 || down["direct"] != 7 || len(up) != 1 {
		t.Fatalf("%v %v %v", up, down, err)
	}
}

func TestTrafficSamplerRatesAndFlush(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _ = st.Migrate(context.Background())
	counter := int64(0)
	src := func(context.Context) (map[string]int64, map[string]int64, error) {
		counter += 1000
		return map[string]int64{"node-a": counter, "direct": counter * 10}, map[string]int64{"node-a": counter * 2}, nil
	}
	s := NewTrafficSampler(st, events.New(8), src, nil)
	ctx := context.Background()
	s.sample(ctx)
	if s.Current().Available != true || s.Current().UpBps != 0 {
		t.Fatalf("first sample %+v", s.Current())
	}
	s.prevAt = s.prevAt.Add(-10 * time.Second)
	s.sample(ctx)
	cur := s.Current()
	if cur.UpBps < 99 || cur.UpBps > 101 || cur.DownBps < 199 || cur.DownBps > 201 {
		t.Fatalf("rates %+v", cur)
	}
	if _, ok := cur.Nodes["direct"]; !ok {
		t.Fatal("direct rate should be reported per node but excluded from totals")
	}
	s.flush(ctx, true)
	up, down, _ := st.TrafficTotals(ctx, time.Now().Add(-time.Hour))
	if up != 1000 || down != 2000 {
		t.Fatalf("totals %d %d (direct must not count)", up, down)
	}
	byNode, _ := st.TrafficByNode(ctx, time.Now().Add(-time.Hour))
	if len(byNode) != 1 || byNode[0].NodeID != "a" {
		t.Fatalf("by node %+v", byNode)
	}
	series, _ := st.TrafficSeries(ctx, time.Now().Add(-time.Hour), 60)
	if len(series) != 1 || series[0].Down != 2000 {
		t.Fatalf("series %+v", series)
	}
	// Counter reset (xray restart) must not produce negative deltas.
	counter = -5000
	s.prevAt = s.prevAt.Add(-10 * time.Second)
	s.sample(ctx)
	if s.Current().UpBps != 0 {
		t.Fatalf("reset handling %+v", s.Current())
	}
}

func TestLatencyRound(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _ = st.Migrate(ctx)
	db := st.DB()
	_, _ = db.Exec(`INSERT INTO nodes (id,name,protocol,address,port,enabled) VALUES ('ok','ok','vless','1.1.1.1',443,1),('bad','bad','vless','2.2.2.2',443,1),('off','off','vless','3.3.3.3',443,0)`)
	p := NewLatencyProber(st, events.New(4), nil)
	p.Dial = func(_ context.Context, addr string) (time.Duration, error) {
		if addr == "1.1.1.1:443" {
			return 42 * time.Millisecond, nil
		}
		return 0, errors.New("timeout")
	}
	res := p.Round(ctx)
	if len(res) != 2 {
		t.Fatalf("results %+v", res)
	}
	nodes, _ := st.ListNodes(ctx)
	for _, n := range nodes {
		switch n.ID {
		case "ok":
			if n.LatencyMs == nil || *n.LatencyMs != 42 {
				t.Fatalf("ok latency %v", n.LatencyMs)
			}
		case "bad":
			if n.LatencyMs == nil || *n.LatencyMs != -1 {
				t.Fatalf("bad latency %v", n.LatencyMs)
			}
		case "off":
			if n.LatencyMs != nil {
				t.Fatal("disabled node must not be probed")
			}
		}
	}
}

func TestLatencyRoundViaXrayUsesSlots(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "l2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _ = st.Migrate(ctx)
	db := st.DB()
	// IDs sort as a < b: a gets slot 0, b slot 1.
	_, _ = db.Exec(`INSERT INTO nodes (id,name,protocol,address,port,enabled) VALUES ('b','wg','wireguard','9.9.9.9',51820,1),('a','vl','vless','1.1.1.1',443,1)`)
	_ = st.SetSetting(ctx, ProbeURLKey, "http://example.test/204")
	p := NewLatencyProber(st, events.New(4), nil)
	p.EnableXray("127.0.0.1:1")
	var seen []string
	var mu sync.Mutex
	p.Probe = func(_ context.Context, user, pass, url string) (time.Duration, error) {
		mu.Lock()
		seen = append(seen, user+"|"+pass+"|"+url)
		mu.Unlock()
		if user == "probe-1" { // the WireGuard node answers
			return 30 * time.Millisecond, nil
		}
		return 0, errors.New("socks: dial failed")
	}
	p.Dial = func(context.Context, string) (time.Duration, error) {
		t.Fatal("tcp fallback must not be used when a slot exists")
		return 0, nil
	}
	res := p.Round(ctx)
	if len(res) != 2 {
		t.Fatalf("results %+v", res)
	}
	sort.Strings(seen)
	if len(seen) != 2 || seen[0] != "probe-0|probe|http://example.test/204" || seen[1] != "probe-1|probe|http://example.test/204" {
		t.Fatalf("probe calls %v", seen)
	}
	last := p.Results()
	if last["b"].Ms != 30 || last["b"].Via != "xray" || last["a"].Ms != -1 || last["a"].Error == "" {
		t.Fatalf("results %+v", last)
	}
	nodes, _ := st.ListNodes(ctx)
	for _, n := range nodes {
		if n.ID == "b" && (n.LatencyMs == nil || *n.LatencyMs != 30) {
			t.Fatalf("wg latency %v", n.LatencyMs)
		}
	}
}
