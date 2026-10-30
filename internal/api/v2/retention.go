package v2

import (
	"net/http"

	"github.com/zlylong/EdgeRouteGW/internal/janitor"
)

func (d Deps) registerRetention(r *router) {
	r.handle(http.MethodGet, "/api/v2/system/retention", d.getRetention)
	r.handle(http.MethodPut, "/api/v2/system/retention", d.putRetention)
	r.handle(http.MethodPost, "/api/v2/system/retention/cleanup", d.cleanupRetention)
}

type retentionView struct {
	Settings janitor.Settings `json:"settings"`
	Usage    janitor.Usage    `json:"usage"`
	Report   *janitor.Report  `json:"report,omitempty"`
}

func (d Deps) retentionView(r *http.Request, rep *janitor.Report) (retentionView, error) {
	u, err := d.Janitor.Usage(r.Context())
	if err != nil {
		return retentionView{}, err
	}
	return retentionView{Settings: d.Janitor.Settings(r.Context()), Usage: u, Report: rep}, nil
}

// getRetention returns the retention settings and what everything they
// govern currently occupies.
func (d Deps) getRetention(w http.ResponseWriter, r *http.Request) {
	if d.Janitor == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "retention is disabled in this mode", nil)
		return
	}
	v, err := d.retentionView(r, nil)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// putRetention saves new settings and applies them at once.
func (d Deps) putRetention(w http.ResponseWriter, r *http.Request) {
	if d.Janitor == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "retention is disabled in this mode", nil)
		return
	}
	var in janitor.Settings
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	if err := in.Validate(); err != nil {
		d.fail(w, r, invalid("settings", "%v", err))
		return
	}
	rep, err := d.Janitor.SetSettings(r.Context(), in)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	v, err := d.retentionView(r, &rep)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// cleanupRetention prunes expired history, truncates the runtime logs,
// compacts the database and vacuums the journal now.
func (d Deps) cleanupRetention(w http.ResponseWriter, r *http.Request) {
	if d.Janitor == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "retention is disabled in this mode", nil)
		return
	}
	rep, err := d.Janitor.Cleanup(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	v, err := d.retentionView(r, &rep)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
