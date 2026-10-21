package v2

import (
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/driver/frr"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/routes"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func (d Deps) registerNetwork(r *router) {
	r.handle(http.MethodGet, "/api/v2/ospf", d.handleOSPF)
	r.handle(http.MethodGet, "/api/v2/routes", d.listRoutes)
	r.handle(http.MethodPost, "/api/v2/routes/reset", d.resetRoutes)
	r.handle(http.MethodGet, "/api/v2/interfaces", d.listInterfaces)
	r.handle(http.MethodPost, "/api/v2/network/preflight", d.preflight)
	r.handle(http.MethodPost, "/api/v2/dns/cache-clear", d.clearDNSCache)
}

// InterfaceView is one host interface for the network page.
type InterfaceView struct {
	Name      string   `json:"name"`
	Up        bool     `json:"up"`
	Loopback  bool     `json:"loopback"`
	Addresses []string `json:"addresses"`
	MAC       string   `json:"mac,omitempty"`
}

func (d Deps) listInterfaces(w http.ResponseWriter, r *http.Request) {
	ifaces, err := net.Interfaces()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	items := make([]InterfaceView, 0, len(ifaces))
	for _, ifi := range ifaces {
		v := InterfaceView{Name: ifi.Name, Up: ifi.Flags&net.FlagUp != 0, Loopback: ifi.Flags&net.FlagLoopback != 0, MAC: ifi.HardwareAddr.String(), Addresses: []string{}}
		if addrs, err := ifi.Addrs(); err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok {
					v.Addresses = append(v.Addresses, ipn.String())
				}
			}
		}
		items = append(items, v)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, listResp[InterfaceView]{Items: items})
}

func (d Deps) sources() routes.Sources {
	var src routes.Sources
	if d.GeoIP != nil {
		src.GeoIP = d.GeoIP
	}
	if d.DefaultIface != nil {
		if di, err := d.DefaultIface(); err == nil {
			src.DefaultIface = di
		}
	}
	if d.GeoSite != nil {
		src.GeoSite = d.GeoSite
	}
	return src
}

func (d Deps) preflight(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	mode := model.Mode(strings.ToUpper(strings.TrimSpace(in.Mode)))
	if !mode.Valid() {
		d.fail(w, r, invalid("mode", "must be A, B or C"))
		return
	}
	snap, err := desired.Load(r.Context(), d.Store, nil)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, routes.Preflight(snap, d.sources(), mode))
}

// OSPFResponse is the OSPF panel.
type OSPFResponse struct {
	Mode           model.Mode     `json:"mode"`
	Enabled        bool           `json:"enabled"`
	ServiceIface   string         `json:"service_iface"`
	LANPrefix      string         `json:"lan_prefix,omitempty"`
	Neighbors      []frr.Neighbor `json:"neighbors"`
	NeighborsError string         `json:"neighbors_error,omitempty"`
	Routes         map[string]int `json:"routes"`
	Resolver       ResolverStats  `json:"resolver"`
}

// ResolverStats summarises the resolve cache.
type ResolverStats struct {
	Cached  int `json:"cached"`
	Expired int `json:"expired"`
	Failing int `json:"failing"`
}

func (d Deps) handleOSPF(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	net, err := d.Store.NetworkConfig(ctx)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	res := OSPFResponse{Mode: net.Mode, Enabled: net.Mode != model.ModeA, ServiceIface: net.ServiceIface, Neighbors: []frr.Neighbor{}, Routes: map[string]int{}}
	if net.ServiceIface != "" && d.IfaceAddr != nil {
		if p, err := d.IfaceAddr(net.ServiceIface); err == nil {
			res.LANPrefix = p.Masked().String()
		}
	}
	if counts, err := d.Store.RouteCounts(ctx); err == nil {
		res.Routes = counts
	}
	for _, k := range []string{routes.StatePublished, routes.StatePending, routes.StateFailed, routes.StateProtected} {
		if _, ok := res.Routes[k]; !ok {
			res.Routes[k] = 0
		}
	}
	if entries, err := d.Store.ListResolveCache(ctx); err == nil {
		now := time.Now()
		for _, e := range entries {
			res.Resolver.Cached++
			if e.Expired(now) {
				res.Resolver.Expired++
			}
			if e.FailCount > 0 {
				res.Resolver.Failing++
			}
		}
	}
	if res.Enabled && d.FRR != nil {
		nb, err := d.FRR.Neighbors(ctx)
		if err != nil {
			res.NeighborsError = err.Error()
		} else {
			res.Neighbors = nb
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (d Deps) listRoutes(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListPublishedRoutes(r.Context(), store.RouteFilter{State: r.URL.Query().Get("state"), Query: r.URL.Query().Get("q"), Limit: queryInt(r, "limit", 500)})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[store.PublishedRoute]{Items: items})
}

// resetRoutes clears the recorded decisions; the next reconcile pass
// recomputes and republishes from scratch. It is not a desired-state
// change, so no generation bump, but the reconciler is woken.
func (d Deps) resetRoutes(w http.ResponseWriter, r *http.Request) {
	if err := d.Store.ClearPublishedRoutes(r.Context()); err != nil {
		d.fail(w, r, err)
		return
	}
	_ = d.Store.AddEvent(r.Context(), model.Event{Level: model.LevelInfo, Module: "routes", Type: "reset", Message: "published routes cleared; republishing", Actor: actorOf(r)})
	d.Reconciler.Notify()
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

func (d Deps) clearDNSCache(w http.ResponseWriter, r *http.Request) {
	if err := d.Store.ClearResolveCache(r.Context()); err != nil {
		d.fail(w, r, err)
		return
	}
	if d.ResolverKick != nil {
		d.ResolverKick()
	}
	d.Reconciler.Notify()
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}
