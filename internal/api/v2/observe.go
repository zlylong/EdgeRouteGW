package v2

import (
	"net/http"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/observe"
	"github.com/zlylong/EdgeRouteGW/internal/trace"
)

func (d Deps) registerObserve(r *router) {
	r.handle(http.MethodGet, "/api/v2/trace", d.handleTrace)
	r.handle(http.MethodGet, "/api/v2/connections", d.handleConnections)
}

func (d Deps) handleTrace(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" || len(target) > 253 {
		d.fail(w, r, invalid("target", "provide a domain or IP"))
		return
	}
	snap, err := desired.Load(r.Context(), d.Store, nil)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, trace.Run(snap, trace.Sources{Site: d.GeoSite, IP: d.GeoIP}, target))
}

func (d Deps) handleConnections(w http.ResponseWriter, r *http.Request) {
	if d.Connections == nil {
		writeJSON(w, http.StatusOK, listResp[observe.Connection]{Items: []observe.Connection{}})
		return
	}
	items := d.Connections.Recent(queryInt(r, "limit", 200), r.URL.Query().Get("q"))
	writeJSON(w, http.StatusOK, listResp[observe.Connection]{Items: items})
}
