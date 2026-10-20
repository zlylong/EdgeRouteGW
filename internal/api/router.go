package api

import (
	"net/http"

	v2 "github.com/zlylong/EdgeRouteGW/internal/api/v2"
	"github.com/zlylong/EdgeRouteGW/internal/web"
)

// Deps is everything the HTTP layer needs; it is the v2 dependency set plus
// nothing else today, kept as its own type so cross-version concerns (auth,
// static assets) have a home.
type Deps = v2.Deps

// NewHandler builds the root handler: middleware, /healthz, /api/v2 and the SPA.
func NewHandler(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Store.DB().PingContext(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, CodeInternal, "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	v2.Register(mux, d)

	// Anything else under /api is a JSON 404, never the SPA fallback.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
	})

	mux.Handle("/", web.Handler())

	return chain(mux, requestID, recoverer(d.Log), securityHeaders, accessLog(d.Log))
}
