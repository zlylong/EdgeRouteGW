package api

import (
	"log/slog"
	"net/http"
	"time"

	v2 "github.com/zlylong/EdgeRouteGW/internal/api/v2"
	"github.com/zlylong/EdgeRouteGW/internal/store"
	"github.com/zlylong/EdgeRouteGW/internal/web"
)

// Deps is everything the HTTP layer needs. Add fields here as subsystems
// land; handlers receive Deps rather than reaching for globals.
type Deps struct {
	Store     *store.Store
	Log       *slog.Logger
	StartedAt time.Time
}

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

	v2.Register(mux, v2.Deps{Store: d.Store, Log: d.Log, StartedAt: d.StartedAt})

	// Anything else under /api is a JSON 404, never the SPA fallback.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
	})

	mux.Handle("/", web.Handler())

	return chain(mux, requestID, recoverer(d.Log), securityHeaders, accessLog(d.Log))
}
