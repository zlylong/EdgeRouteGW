package v2

import (
	"errors"
	"net/http"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/update"
)

func (d Deps) registerComponents(r *router) {
	r.handle(http.MethodGet, "/api/v2/components", d.listComponents)
	r.handle(http.MethodGet, "/api/v2/components/{name}/versions", d.componentVersions)
	r.handle(http.MethodPost, "/api/v2/components/{name}/update", d.updateComponent)
	r.handle(http.MethodPost, "/api/v2/components/{name}/rollback", d.rollbackComponent)
	r.handle(http.MethodPost, "/api/v2/components/{name}/install-bundled", d.installBundledComponent)
	r.handle(http.MethodPost, "/api/v2/components/{name}/upload", d.uploadComponent)
	r.handle(http.MethodGet, "/api/v2/components/download-settings", d.getDownloadSettings)
	r.handle(http.MethodPut, "/api/v2/components/download-settings", d.putDownloadSettings)
	r.handle(http.MethodGet, "/api/v2/components/geodata-schedule", d.getGeodataSchedule)
	r.handle(http.MethodPut, "/api/v2/components/geodata-schedule", d.putGeodataSchedule)
	r.handle(http.MethodGet, "/api/v2/components/{name}/schedule", d.getComponentSchedule)
	r.handle(http.MethodPut, "/api/v2/components/{name}/schedule", d.putComponentSchedule)
}

func (d Deps) listComponents(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeJSON(w, http.StatusOK, listResp[update.Status]{Items: []update.Status{}})
		return
	}
	writeJSON(w, http.StatusOK, listResp[update.Status]{Items: d.Updater.Statuses(r.Context(), r.URL.Query().Get("refresh") == "1")})
}

func (d Deps) componentVersions(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "updates are disabled", nil)
		return
	}
	rels, err := d.Updater.Versions(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "UPSTREAM", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, listResp[update.Release]{Items: rels})
}

func (d Deps) updateComponent(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "updates are disabled", nil)
		return
	}
	var in struct {
		Version string `json:"version"`
	}
	if r.ContentLength > 0 {
		if err := decode(r, &in); err != nil {
			d.fail(w, r, err)
			return
		}
	}
	job, err := d.Updater.Start(r.Context(), r.PathValue("name"), strings.TrimSpace(in.Version), "update")
	switch {
	case errors.Is(err, update.ErrBusy):
		writeErr(w, http.StatusConflict, codeConflict, err.Error(), nil)
	case err != nil:
		writeErr(w, http.StatusBadGateway, "UPSTREAM", err.Error(), nil)
	default:
		writeJSON(w, http.StatusAccepted, itemResp[*update.Job]{Item: job})
	}
}

func (d Deps) rollbackComponent(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "updates are disabled", nil)
		return
	}
	job, err := d.Updater.Start(r.Context(), r.PathValue("name"), "", "rollback")
	switch {
	case errors.Is(err, update.ErrBusy):
		writeErr(w, http.StatusConflict, codeConflict, err.Error(), nil)
	case err != nil:
		d.fail(w, r, invalid("name", "%v", err))
	default:
		writeJSON(w, http.StatusAccepted, itemResp[*update.Job]{Item: job})
	}
}

func (d Deps) getGeodataSchedule(w http.ResponseWriter, r *http.Request) {
	sched := "off"
	if d.Updater != nil {
		sched = d.Updater.Schedule(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]string{"schedule": sched})
}

func (d Deps) putGeodataSchedule(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "updates are disabled", nil)
		return
	}
	var in struct {
		Schedule string `json:"schedule"`
	}
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	if err := d.Updater.SetSchedule(r.Context(), in.Schedule); err != nil {
		d.fail(w, r, invalid("schedule", "%v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"schedule": d.Updater.Schedule(r.Context())})
}

func (d Deps) installBundledComponent(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "updates are disabled", nil)
		return
	}
	job, err := d.Updater.Start(r.Context(), r.PathValue("name"), "", "bundled")
	switch {
	case errors.Is(err, update.ErrBusy):
		writeErr(w, http.StatusConflict, codeConflict, err.Error(), nil)
	case err != nil:
		d.fail(w, r, invalid("name", "%v", err))
	default:
		writeJSON(w, http.StatusAccepted, itemResp[*update.Job]{Item: job})
	}
}

// uploadComponent accepts a multipart form with "file" (the official
// release zip) and optional "sha256".
func (d Deps) uploadComponent(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "updates are disabled", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 600<<20) // archive limit (512 MB) plus form overhead
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		d.fail(w, r, invalid("file", "multipart form expected: %v", err))
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		d.fail(w, r, invalid("file", "file is required"))
		return
	}
	defer f.Close()
	sum := strings.ToLower(strings.TrimSpace(r.FormValue("sha256")))
	if sum != "" && len(sum) != 64 {
		d.fail(w, r, invalid("sha256", "must be 64 hex characters"))
		return
	}
	job, err := d.Updater.InstallUpload(r.Context(), r.PathValue("name"), f, hdr.Size, sum)
	switch {
	case errors.Is(err, update.ErrBusy):
		writeErr(w, http.StatusConflict, codeConflict, err.Error(), nil)
	case err != nil:
		d.fail(w, r, invalid("file", "%v", err))
	default:
		writeJSON(w, http.StatusAccepted, itemResp[*update.Job]{Item: job})
	}
}

func (d Deps) getDownloadSettings(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeJSON(w, http.StatusOK, update.DownloadSettings{})
		return
	}
	writeJSON(w, http.StatusOK, d.Updater.DownloadSettingsView(r.Context()))
}

func (d Deps) putDownloadSettings(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "updates are disabled", nil)
		return
	}
	var in update.DownloadSettings
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	if err := d.Updater.SetDownloadSettings(r.Context(), in); err != nil {
		d.fail(w, r, invalid("proxy", "%v", err))
		return
	}
	writeJSON(w, http.StatusOK, d.Updater.DownloadSettingsView(r.Context()))
}

func (d Deps) getComponentSchedule(w http.ResponseWriter, r *http.Request) {
	sched := "off"
	if d.Updater != nil {
		sched = d.Updater.ScheduleOf(r.Context(), r.PathValue("name"))
	}
	writeJSON(w, http.StatusOK, map[string]string{"schedule": sched})
}

func (d Deps) putComponentSchedule(w http.ResponseWriter, r *http.Request) {
	if d.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "updates are disabled", nil)
		return
	}
	var in struct {
		Schedule string `json:"schedule"`
	}
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	name := r.PathValue("name")
	if err := d.Updater.SetScheduleOf(r.Context(), name, in.Schedule); err != nil {
		d.fail(w, r, invalid("schedule", "%v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"schedule": d.Updater.ScheduleOf(r.Context(), name)})
}
