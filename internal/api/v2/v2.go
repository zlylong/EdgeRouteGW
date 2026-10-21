// Package v2 holds the /api/v2 handlers. One file per resource family.
// Handlers decode, validate, call the store (inside Mutate for writes), then
// notify the reconciler; they never touch Xray, Mosdns or nftables.
package v2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/auth"
	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/frr"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/geo"
	"github.com/zlylong/EdgeRouteGW/internal/observe"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/provision"
	"github.com/zlylong/EdgeRouteGW/internal/reconcile"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
	"github.com/zlylong/EdgeRouteGW/internal/update"
)

// Reconciler is the slice of reconcile.Reconciler the API uses.
type Reconciler interface {
	Notify()
	Status() reconcile.Status
	PlanOnly(ctx context.Context) ([]driver.Plan, *render.Artifacts, error)
	ReconcileNow(ctx context.Context) error
}

// Deps is everything the v2 handlers need.
type Deps struct {
	Store      *store.Store
	Keyring    *secret.Keyring
	Reconciler Reconciler
	Bus        *events.Bus
	Paths      paths.Paths
	Log        *slog.Logger
	StartedAt  time.Time
	// Auth is required: login/logout/me and the middleware come from it.
	Auth *auth.Service
	// Optional runtime sources.
	GeoSite     *geo.Site
	GeoIP       *geo.IP
	Connections *observe.ConnTracker
	// FRR exposes OSPF neighbours; nil without drivers.
	FRR *frr.Driver
	// ResolverKick wakes the resolver after writes that may add domains.
	ResolverKick func()
	// IfaceAddr reads an interface's IPv4 prefix (for the network page).
	IfaceAddr render.InterfaceAddr
	// DefaultIface names the default-route interface (preflight fallback).
	DefaultIface func() (string, error)
	// Provision deploys tunnel servers on managed hosts; nil disables.
	Provision *provision.Service
	// Updater installs components; nil disables.
	Updater *update.Service
	// Traffic and Latency are the telemetry collectors; nil when not running.
	Traffic *observe.TrafficSampler
	Latency *observe.LatencyProber
}

// Register mounts every v2 route on mux.
func Register(mux *http.ServeMux, d Deps) {
	r := newRouter(mux)
	d.registerAuth(r)
	d.registerObserve(r)
	d.registerNetwork(r)
	d.registerProvision(r)
	d.registerComponents(r)
	d.registerBackup(r)
	d.registerTelemetry(r)
	r.handle(http.MethodGet, "/api/v2/state", d.handleState)
	r.handle(http.MethodGet, "/api/v2/plan", d.handlePlan)
	r.handle(http.MethodPost, "/api/v2/reconcile", d.handleReconcileNow)
	r.handle(http.MethodGet, "/api/v2/artifacts/{component}", d.handleArtifact)
	r.handle(http.MethodGet, "/api/v2/events", d.handleEvents)
	r.handle(http.MethodGet, "/api/v2/events/stream", d.handleStream)
	r.handle(http.MethodGet, "/api/v2/changesets", d.handleChangesets)
	r.handle(http.MethodGet, "/api/v2/apply-log", d.handleApplyLog)

	r.handle(http.MethodGet, "/api/v2/nodes", d.listNodes)
	r.handle(http.MethodPost, "/api/v2/nodes", d.createNode)
	r.handle(http.MethodPost, "/api/v2/node-imports", d.importNodes)
	r.handle(http.MethodGet, "/api/v2/nodes/{id}", d.getNode)
	r.handle(http.MethodPut, "/api/v2/nodes/{id}", d.updateNode)
	r.handle(http.MethodDelete, "/api/v2/nodes/{id}", d.deleteNode)
	r.handle(http.MethodPost, "/api/v2/nodes/{id}/default", d.setDefaultNode)
	r.handle(http.MethodPost, "/api/v2/nodes/{id}/enabled", d.setNodeEnabled)

	r.handle(http.MethodGet, "/api/v2/groups", d.listGroups)
	r.handle(http.MethodPost, "/api/v2/groups", d.createGroup)
	r.handle(http.MethodGet, "/api/v2/groups/{id}", d.getGroup)
	r.handle(http.MethodPut, "/api/v2/groups/{id}", d.updateGroup)
	r.handle(http.MethodDelete, "/api/v2/groups/{id}", d.deleteGroup)

	r.handle(http.MethodGet, "/api/v2/rule-sets", d.listRuleSets)
	r.handle(http.MethodPost, "/api/v2/rule-sets", d.createRuleSet)
	r.handle(http.MethodPost, "/api/v2/rule-set-order", d.reorderRuleSets)
	r.handle(http.MethodPut, "/api/v2/rule-sets/{id}", d.updateRuleSet)
	r.handle(http.MethodDelete, "/api/v2/rule-sets/{id}", d.deleteRuleSet)
	r.handle(http.MethodPost, "/api/v2/rule-sets/{id}/reorder", d.reorderRules)

	r.handle(http.MethodGet, "/api/v2/rules", d.listRules)
	r.handle(http.MethodGet, "/api/v2/geo/tags", d.geoTags)
	r.handle(http.MethodPost, "/api/v2/rules", d.createRules)
	r.handle(http.MethodPut, "/api/v2/rules/{id}", d.updateRule)
	r.handle(http.MethodDelete, "/api/v2/rules/{id}", d.deleteRule)
	r.handle(http.MethodPost, "/api/v2/rules/{id}/move", d.moveRule)

	r.handle(http.MethodGet, "/api/v2/device-policies", d.listDevicePolicies)
	r.handle(http.MethodPost, "/api/v2/device-policies", d.createDevicePolicy)
	r.handle(http.MethodPut, "/api/v2/device-policies/{id}", d.updateDevicePolicy)
	r.handle(http.MethodDelete, "/api/v2/device-policies/{id}", d.deleteDevicePolicy)

	r.handle(http.MethodGet, "/api/v2/bypass", d.listBypass)
	r.handle(http.MethodPost, "/api/v2/bypass", d.createBypass)
	r.handle(http.MethodDelete, "/api/v2/bypass/{id}", d.deleteBypass)

	r.handle(http.MethodGet, "/api/v2/dns", d.getDNS)
	r.handle(http.MethodPut, "/api/v2/dns", d.putDNS)
	r.handle(http.MethodGet, "/api/v2/network", d.getNetwork)
	r.handle(http.MethodPut, "/api/v2/network", d.putNetwork)
	r.finish()
}

// router wraps ServeMux so a known path with the wrong method yields 405 +
// Allow instead of falling into the "/api/" JSON-404 catch-all.
type router struct {
	mux     *http.ServeMux
	methods map[string][]string
}

func newRouter(mux *http.ServeMux) *router {
	return &router{mux: mux, methods: map[string][]string{}}
}

func (r *router) handle(method, path string, h http.HandlerFunc) {
	r.mux.HandleFunc(method+" "+path, h)
	r.methods[path] = append(r.methods[path], method)
}

func (r *router) finish() {
	for path, methods := range r.methods {
		sort.Strings(methods)
		allow := strings.Join(methods, ", ")
		r.mux.HandleFunc(path, func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Allow", allow)
			writeErr(w, http.StatusMethodNotAllowed, codeMethod, "method not allowed; see Allow header", nil)
		})
	}
}

// --- responses ---

const (
	codeBadRequest = "BAD_REQUEST"
	codeValidation = "VALIDATION_FAILED"
	codeNotFound   = "NOT_FOUND"
	codeConflict   = "CONFLICT"
	codeInternal   = "INTERNAL_ERROR"
	codeMethod     = "METHOD_NOT_ALLOWED"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg, Details: details}})
}

// validationError carries field-level problems to the client.
type validationError struct {
	Field string
	Msg   string
}

func (v *validationError) Error() string { return v.Field + ": " + v.Msg }

func invalid(field, format string, args ...any) error {
	return &validationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// fail maps an error to a response. Validation and not-found are client
// errors; anything else is logged and reported as internal.
func (d Deps) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *validationError
	switch {
	case errors.As(err, &ve):
		writeErr(w, http.StatusUnprocessableEntity, codeValidation, ve.Error(), map[string]any{"field": ve.Field})
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, codeNotFound, "not found", nil)
	case isConstraint(err):
		writeErr(w, http.StatusConflict, codeConflict, conflictMessage(err), nil)
	default:
		d.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeErr(w, http.StatusInternalServerError, codeInternal, "internal error", nil)
	}
}

func isConstraint(err error) bool {
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") || strings.Contains(s, "FOREIGN KEY constraint failed") || strings.Contains(s, "CHECK constraint failed")
}

func conflictMessage(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "UNIQUE"):
		return "an entry with the same value already exists"
	case strings.Contains(s, "FOREIGN KEY"):
		return "referenced object does not exist"
	default:
		return "constraint violated"
	}
}

const maxBody = 1 << 20

// decode reads a JSON body strictly: unknown fields and trailing data are
// errors, so a typo in a field name cannot silently become a no-op.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("body", "invalid JSON: %v", err)
	}
	if dec.More() {
		return invalid("body", "unexpected trailing data")
	}
	return nil
}

// mutate runs a desired-state write and wakes the reconciler.
func (d Deps) mutate(ctx context.Context, r *http.Request, summary string, fn func(tx *sql.Tx) error) (int64, error) {
	gen, err := d.Store.Mutate(ctx, store.Mutation{Actor: actorOf(r), Summary: summary}, fn)
	if err != nil {
		return 0, err
	}
	d.Bus.Publish(events.StateChanged, map[string]any{"generation": gen, "summary": summary})
	d.Reconciler.Notify()
	if d.ResolverKick != nil {
		d.ResolverKick()
	}
	return gen, nil
}

// actorOf names who made a change: the authenticated principal, or the
// remote address when the handler runs without the auth middleware (tests).
func actorOf(r *http.Request) string {
	if p, ok := auth.FromContext(r.Context()); ok {
		return p.Actor()
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func queryInt64(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	return n
}

type listResp[T any] struct {
	Items []T `json:"items"`
}

type itemResp[T any] struct {
	Item       T     `json:"item"`
	Generation int64 `json:"generation,omitempty"`
}

type genResp struct {
	Generation int64 `json:"generation"`
	Deleted    int   `json:"deleted,omitempty"`
	Retargeted int64 `json:"retargeted_rules,omitempty"`
}
