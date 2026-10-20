package v2

import (
	"net/http"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/model"
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
	Components            []model.ComponentHealth `json:"components"`
	StartedAt             time.Time               `json:"started_at"`
	UptimeSeconds         int64                   `json:"uptime_s"`
	SchemaVersion         string                  `json:"schema_version"`
}

func (d Deps) handleState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	meta, err := d.Store.Meta(ctx)
	if err != nil {
		d.internalError(w, r, "read state", err)
		return
	}
	net, err := d.Store.NetworkConfig(ctx)
	if err != nil {
		d.internalError(w, r, "read network config", err)
		return
	}
	schema, err := d.Store.SchemaVersion(ctx)
	if err != nil {
		d.internalError(w, r, "read schema version", err)
		return
	}

	// M0: no drivers yet, so every component reports unknown. The reconciler
	// (M1) will own this list.
	comps := make([]model.ComponentHealth, 0, len(model.Components))
	for _, c := range model.Components {
		comps = append(comps, model.ComponentHealth{Name: c, Status: model.StatusUnknown})
	}

	now := time.Now()
	writeJSON(w, http.StatusOK, StateResponse{
		Version:               version.Get(),
		Mode:                  net.Mode,
		Generation:            meta.Generation,
		LastAppliedGeneration: meta.LastAppliedGeneration,
		Pending:               meta.Generation != meta.LastAppliedGeneration,
		Components:            comps,
		StartedAt:             d.StartedAt.UTC(),
		UptimeSeconds:         int64(now.Sub(d.StartedAt).Seconds()),
		SchemaVersion:         schema,
	})
}
