// Package render turns a desired-state Snapshot into the exact bytes each
// component consumes. Everything here is a pure function of the Snapshot and
// Deps: no I/O beyond reading geodata, no clocks, no randomness, so the same
// input always yields byte-identical Artifacts. That property is what lets
// the reconciler skip restarts and show real diffs.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/geo"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/routes"
)

// SiteSource is the geosite lookup the renderer needs. nil means "no
// geodata installed": geosite rules still reach Xray (which has its own
// reader) but cannot be expanded for Mosdns.
type SiteSource interface {
	Entries(tag string) ([]geo.Entry, error)
}

// IPSource is the geoip lookup for Mode B/C route publishing; nil means
// not installed.
type IPSource = routes.IPSource

// Deps are the renderer's inputs besides the Snapshot.
type Deps struct {
	Paths   paths.Paths
	GeoSite SiteSource
	GeoIP   IPSource
	// IfaceAddr resolves the service interface's address for the FRR
	// config; nil disables the OSPF network statement.
	IfaceAddr InterfaceAddr
}

// Artifacts is everything the drivers apply for one generation.
type Artifacts struct {
	Generation int64
	Mode       model.Mode

	// Xray is the full config.json. XrayOutbounds/XrayRules are the same
	// content split so the driver can decide between a hot update and a
	// restart; XrayStructuralHash covers everything else.
	Xray               []byte
	XrayOutbounds      map[string]json.RawMessage // tag -> outbound object
	XrayRules          json.RawMessage            // routing.rules array
	XrayStructuralHash string

	MosdnsConfig       []byte
	MosdnsProxyDomains []byte
	// MosdnsCNDomains is geosite:cn expanded for the local-upstream match.
	// nil when no geodata is available (the driver then keeps whatever file
	// exists).
	MosdnsCNDomains []byte

	Nft []byte

	// Routes is the IPv4 prefix set to publish (Mode B/C; empty in A), with
	// the candidates policy rejected and the domains the resolver must keep
	// fresh. FRR is the frr.conf for the OSPF side (nil in Mode A).
	Routes         []routes.Route
	RouteRejected  []routes.Route
	ResolveDomains []string
	FRR            []byte

	// Warnings are non-fatal findings (a rule skipped, a group with one live
	// member) surfaced to the UI alongside the apply.
	Warnings []string
}

// Render produces Artifacts for snap.
func Render(snap *desired.Snapshot, d Deps) (*Artifacts, error) {
	a := &Artifacts{Generation: snap.Generation, Mode: snap.Network.Mode}
	if !a.Mode.Valid() {
		return nil, fmt.Errorf("invalid mode %q", a.Mode)
	}
	if err := renderXray(snap, d, a); err != nil {
		return nil, fmt.Errorf("render xray: %w", err)
	}
	if err := renderMosdns(snap, d, a); err != nil {
		return nil, fmt.Errorf("render mosdns: %w", err)
	}
	if err := renderNft(snap, d, a); err != nil {
		return nil, fmt.Errorf("render nftables: %w", err)
	}
	if err := renderRoutes(snap, d, a); err != nil {
		return nil, fmt.Errorf("render routes: %w", err)
	}
	if a.Warnings == nil {
		a.Warnings = []string{}
	}
	sort.Strings(a.Warnings)
	return a, nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func (a *Artifacts) warn(format string, args ...any) {
	a.Warnings = append(a.Warnings, fmt.Sprintf(format, args...))
}
