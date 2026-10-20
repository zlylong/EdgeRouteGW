// Package v2 holds the /api/v2 handlers. One file per resource.
package v2

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Deps is the subset of api.Deps the v2 handlers use.
type Deps struct {
	Store     *store.Store
	Log       *slog.Logger
	StartedAt time.Time
}

// Register mounts every v2 route on mux.
func Register(mux *http.ServeMux, d Deps) {
	r := newRouter(mux)
	r.handle(http.MethodGet, "/api/v2/state", d.handleState)
	r.finish()
}

// router wraps ServeMux so that a request with a known path but wrong method
// gets a 405 with an Allow header. Plain ServeMux only does that when no other
// pattern matches, and the "/api/" JSON-404 catch-all always matches, which
// would turn every wrong-method call into a misleading NOT_FOUND.
type router struct {
	mux     *http.ServeMux
	methods map[string][]string // path -> allowed methods
}

func newRouter(mux *http.ServeMux) *router {
	return &router{mux: mux, methods: map[string][]string{}}
}

func (r *router) handle(method, path string, h http.HandlerFunc) {
	r.mux.HandleFunc(method+" "+path, h)
	r.methods[path] = append(r.methods[path], method)
}

// finish registers the method-less fallback for every path seen so far.
func (r *router) finish() {
	for path, methods := range r.methods {
		sort.Strings(methods)
		allow := strings.Join(methods, ", ")
		r.mux.HandleFunc(path, func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Allow", allow)
			writeJSON(w, http.StatusMethodNotAllowed, errorBody{Error: errorDetail{Code: "METHOD_NOT_ALLOWED", Message: "method not allowed; see Allow header"}})
		})
	}
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (d Deps) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	d.Log.Error(what, "err", err, "path", r.URL.Path)
	writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorDetail{Code: "INTERNAL_ERROR", Message: what}})
}
