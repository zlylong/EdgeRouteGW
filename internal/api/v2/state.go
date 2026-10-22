package v2

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/store"
	"github.com/zlylong/EdgeRouteGW/internal/version"
)

// StateResponse is the one-call overview the UI loads first and the SSE
// stream keeps current afterwards.
type StateResponse struct {
	Version               version.Info            `json:"version"`
	Mode                  model.Mode              `json:"mode"`
	Generation            int64                   `json:"generation"`
	LastAppliedGeneration int64                   `json:"last_applied_generation"`
	Pending               bool                    `json:"pending"`
	Applying              bool                    `json:"applying"`
	LastError             string                  `json:"last_error,omitempty"`
	RetryInSeconds        int                     `json:"retry_in_s,omitempty"`
	Components            []model.ComponentHealth `json:"components"`
	Warnings              []string                `json:"warnings"`
	StartedAt             time.Time               `json:"started_at"`
	UptimeSeconds         int64                   `json:"uptime_s"`
	SchemaVersion         string                  `json:"schema_version"`
}

func (d Deps) buildState(r *http.Request) (StateResponse, error) {
	ctx := r.Context()
	meta, err := d.Store.Meta(ctx)
	if err != nil {
		return StateResponse{}, err
	}
	net, err := d.Store.NetworkConfig(ctx)
	if err != nil {
		return StateResponse{}, err
	}
	schema, err := d.Store.SchemaVersion(ctx)
	if err != nil {
		return StateResponse{}, err
	}
	st := d.Reconciler.Status()
	now := time.Now()
	return StateResponse{
		Version:               version.Get(),
		Mode:                  net.Mode,
		Generation:            meta.Generation,
		LastAppliedGeneration: meta.LastAppliedGeneration,
		Pending:               meta.Generation != meta.LastAppliedGeneration || st.Applying,
		Applying:              st.Applying,
		LastError:             st.LastError,
		RetryInSeconds:        st.RetryInSeconds,
		Components:            st.Components,
		Warnings:              st.Warnings,
		StartedAt:             d.StartedAt.UTC(),
		UptimeSeconds:         int64(now.Sub(d.StartedAt).Seconds()),
		SchemaVersion:         schema,
	}, nil
}

func (d Deps) handleState(w http.ResponseWriter, r *http.Request) {
	s, err := d.buildState(r)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// PlanResponse is a dry run: what every driver would do right now.
type PlanResponse struct {
	Generation int64          `json:"generation"`
	Plans      []driver.Plan  `json:"plans"`
	Warnings   []string       `json:"warnings"`
	Artifacts  map[string]int `json:"artifact_sizes"`
}

func (d Deps) handlePlan(w http.ResponseWriter, r *http.Request) {
	plans, arts, err := d.Reconciler.PlanOnly(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, PlanResponse{
		Generation: arts.Generation,
		Plans:      plans,
		Warnings:   arts.Warnings,
		Artifacts: map[string]int{
			"xray": len(arts.Xray), "mosdns": len(arts.MosdnsConfig),
			"mosdns-domains": len(arts.MosdnsProxyDomains), "nftables": len(arts.Nft),
		},
	})
}

func (d Deps) handleReconcileNow(w http.ResponseWriter, r *http.Request) {
	err := d.Reconciler.ReconcileNow(r.Context())
	s, serr := d.buildState(r)
	if serr != nil {
		d.fail(w, r, serr)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"state": s, "error": errorDetail{Code: "APPLY_FAILED", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s})
}

// handleArtifact returns a rendered artifact as text for the "generated
// config" view. It renders fresh from desired state, so it shows what the
// next apply will produce.
func (d Deps) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if !sessionOnly(w, r) { // rendered configs carry node credentials
		return
	}
	_, arts, err := d.Reconciler.PlanOnly(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var body []byte
	switch r.PathValue("component") {
	case "xray":
		body = arts.Xray
	case "mosdns":
		body = arts.MosdnsConfig
	case "mosdns-domains":
		body = arts.MosdnsProxyDomains
	case "nftables":
		body = arts.Nft
	default:
		writeErr(w, http.StatusNotFound, codeNotFound, "unknown artifact; use xray|mosdns|mosdns-domains|nftables", nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (d Deps) handleEvents(w http.ResponseWriter, r *http.Request) {
	f := store.EventFilter{
		Module:   r.URL.Query().Get("module"),
		Level:    model.EventLevel(r.URL.Query().Get("level")),
		BeforeID: queryInt64(r, "before_id"),
		Limit:    queryInt(r, "limit", 200),
	}
	items, err := d.Store.ListEvents(r.Context(), f)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[model.Event]{Items: items})
}

func (d Deps) handleChangesets(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListChangesets(r.Context(), queryInt(r, "limit", 100))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[model.Changeset]{Items: items})
}

func (d Deps) handleApplyLog(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListApplyLog(r.Context(), queryInt64(r, "generation"), queryInt(r, "limit", 200))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[model.ApplyLogEntry]{Items: items})
}

// handleStream is the SSE feed. It opens with a full state snapshot, then
// relays bus events, with a heartbeat so proxies keep the connection alive.
func (d Deps) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, codeInternal, "streaming unsupported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(t events.Type, data any) bool {
		b, err := jsonMarshal(data)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", t, b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if s, err := d.buildState(r); err == nil {
		if !write("state", s) {
			return
		}
	}
	ctx := r.Context()
	ch := d.Bus.Subscribe(ctx)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !write(ev.Type, ev.Data) {
				return
			}
			// After an apply finishes or fails the state changed; push a
			// fresh snapshot so clients need no extra round trip.
			if ev.Type == events.ApplyFinished || ev.Type == events.ApplyFailed || ev.Type == events.StateChanged {
				if s, err := d.buildState(r); err == nil && !write("state", s) {
					return
				}
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": ping %d\n\n", time.Now().Unix()); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func jsonMarshal(v any) ([]byte, error) {
	var sb strings.Builder
	enc := jsonEncoder(&sb)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(sb.String(), "\n")), nil
}
