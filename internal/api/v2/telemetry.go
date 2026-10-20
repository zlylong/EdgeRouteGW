package v2

import (
	"net/http"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/auth"
	"github.com/zlylong/EdgeRouteGW/internal/observe"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func (d Deps) registerTelemetry(r *router) {
	r.handle(http.MethodGet, "/api/v2/traffic", d.handleTraffic)
	r.handle(http.MethodPost, "/api/v2/node-probes", d.probeNodes)
	r.handle(http.MethodPost, "/api/v2/changesets/{id}/revert", d.revertChangeset)
}

// TrafficResponse is the history + current rates for one range.
type TrafficResponse struct {
	Range     string               `json:"range"`
	BucketSec int64                `json:"bucket_s"`
	Series    []store.TrafficPoint `json:"series"`
	TotalUp   int64                `json:"total_up"`
	TotalDown int64                `json:"total_down"`
	Nodes     []store.NodeTraffic  `json:"nodes"`
	Current   observe.Rates        `json:"current"`
}

func (d Deps) handleTraffic(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	var since time.Duration
	var bucket int64
	switch rng {
	case "24h":
		since, bucket = 24*time.Hour, 600
	case "7d":
		since, bucket = 7*24*time.Hour, 3600
	case "30d":
		since, bucket = 30*24*time.Hour, 6*3600
	default:
		rng, since, bucket = "1h", time.Hour, 60
	}
	ctx := r.Context()
	from := time.Now().Add(-since)
	series, err := d.Store.TrafficSeries(ctx, from, bucket)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	up, down, err := d.Store.TrafficTotals(ctx, from)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	nodes, err := d.Store.TrafficByNode(ctx, from)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	res := TrafficResponse{Range: rng, BucketSec: bucket, Series: series, TotalUp: up, TotalDown: down, Nodes: nodes, Current: observe.Rates{Nodes: map[string]observe.Rate{}}}
	if d.Traffic != nil {
		res.Current = d.Traffic.Current()
		if res.Current.Nodes == nil {
			res.Current.Nodes = map[string]observe.Rate{}
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// probeNodes runs one latency round synchronously and returns the results.
func (d Deps) probeNodes(w http.ResponseWriter, r *http.Request) {
	if d.Latency == nil {
		writeJSON(w, http.StatusOK, listResp[observe.LatencyResult]{Items: []observe.LatencyResult{}})
		return
	}
	results := d.Latency.Round(r.Context())
	if results == nil {
		results = []observe.LatencyResult{}
	}
	writeJSON(w, http.StatusOK, listResp[observe.LatencyResult]{Items: results})
}

func (d Deps) revertChangeset(w http.ResponseWriter, r *http.Request) {
	actor := actorOf(r)
	if p, ok := auth.FromContext(r.Context()); ok {
		actor = p.Actor()
	}
	gen, restored, err := d.Store.RevertTo(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		if restored > 0 {
			d.fail(w, r, invalid("id", "%v", err))
			return
		}
		d.fail(w, r, err)
		return
	}
	d.Bus.Publish("state.changed", map[string]any{"generation": gen, "summary": "revert"})
	d.Reconciler.Notify()
	if d.ResolverKick != nil {
		d.ResolverKick()
	}
	writeJSON(w, http.StatusOK, map[string]any{"generation": gen, "restored_generation": restored})
}
