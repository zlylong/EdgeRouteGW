package api

import (
	"net/http"
	"strings"

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

	apiMux := http.NewServeMux()
	v2.Register(apiMux, d)
	// Anything else under /api is a JSON 404, never the SPA fallback.
	apiMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
	})
	var apiHandler http.Handler = apiMux
	if d.Auth != nil {
		apiHandler = d.Auth.Middleware(func(r *http.Request) bool {
			// Login must be reachable unauthenticated; /me answers for both.
			return r.URL.Path == "/api/v2/auth/login" || r.URL.Path == "/api/v2/auth/me"
		})(apiMux)
	}
	mux.Handle("/api/", apiHandler)

	mux.Handle("/", web.Handler())

	_ = strings.HasPrefix
	return chain(mux, requestID, recoverer(d.Log), securityHeaders, accessLog(d.Log))
}
