package v2

import (
	"net/http"

	"github.com/zlylong/EdgeRouteGW/internal/tune"
)

func (d Deps) registerTuning(r *router) {
	r.handle(http.MethodGet, "/api/v2/tuning", d.getTuning)
	r.handle(http.MethodPost, "/api/v2/tuning/apply", d.applyTuning)
}

// getTuning returns the last environment tuning report (inspecting now when
// the service has not run one yet).
func (d Deps) getTuning(w http.ResponseWriter, r *http.Request) {
	if d.Tuner == nil {
		writeJSON(w, http.StatusOK, tune.Report{Items: []tune.Item{}})
		return
	}
	rep := d.Tuner.Last()
	if rep == nil {
		rep = d.Tuner.Run(r.Context(), false)
	}
	writeJSON(w, http.StatusOK, rep)
}

// applyTuning re-inspects and applies everything that can be applied.
func (d Deps) applyTuning(w http.ResponseWriter, r *http.Request) {
	if d.Tuner == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "tuning is disabled in this mode", nil)
		return
	}
	writeJSON(w, http.StatusOK, d.Tuner.Run(r.Context(), true))
}
