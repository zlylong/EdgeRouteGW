package render

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// Kernel WireGuard: a WireGuard node becomes a kernel interface plus a
// routing table reached through a packet mark; Xray's outbound for the node
// is a plain freedom outbound carrying that mark, so the tunnel crypto runs
// in the kernel instead of Xray's userspace stack (which tops out around a
// quarter of a gigabit per core).
const (
	WGIfacePrefix = "ergw-"
	WGTableBase   = 300
	WGMarkBase    = 0x100
	WGMaxIfaces   = 64
	WGKeepalive   = 25
	WGDefaultMTU  = 1420
)

// WGPeer is the remote side of a kernel WireGuard interface.
type WGPeer struct {
	PublicKey    string   `json:"public_key"`
	PresharedKey string   `json:"-"`
	Endpoint     string   `json:"endpoint"`
	AllowedIPs   []string `json:"allowed_ips"`
	Keepalive    int      `json:"keepalive"`
}

// WGIface is one kernel interface the wg driver maintains.
type WGIface struct {
	NodeID     string   `json:"node_id"`
	Name       string   `json:"name"`
	PrivateKey string   `json:"-"`
	Addresses  []string `json:"addresses"`
	MTU        int      `json:"mtu"`
	Peers      []WGPeer `json:"peers"`
	Table      int      `json:"table"`
	Mark       int      `json:"mark"`
}

// WGIfaceName derives a stable, 13-character interface name from a node id.
func WGIfaceName(nodeID string) string {
	sum := sha1.Sum([]byte(nodeID))
	return WGIfacePrefix + hex.EncodeToString(sum[:4])
}

type wgSettings struct {
	Address  []string `json:"address"`
	MTU      int      `json:"mtu"`
	Reserved []int    `json:"reserved"`
	Peers    []struct {
		PublicKey    string   `json:"publicKey"`
		PreSharedKey string   `json:"preSharedKey"`
		Endpoint     string   `json:"endpoint"`
		AllowedIPs   []string `json:"allowedIPs"`
		KeepAlive    int      `json:"keepAlive"`
	} `json:"peers"`
}

// kernelWGIface converts a node into an interface definition, or reports why
// the node must stay on Xray's userspace implementation.
func kernelWGIface(n model.Node) (WGIface, error) {
	if strings.ToLower(string(n.Protocol)) != "wireguard" {
		return WGIface{}, fmt.Errorf("not wireguard")
	}
	if n.Credential == "" {
		return WGIface{}, fmt.Errorf("no private key")
	}
	var raw struct {
		Settings wgSettings `json:"settings"`
	}
	if err := json.Unmarshal([]byte(n.Params), &raw); err != nil {
		return WGIface{}, fmt.Errorf("params: %w", err)
	}
	s := raw.Settings
	if len(s.Reserved) > 0 {
		return WGIface{}, fmt.Errorf("reserved bytes (WARP-style) are only supported by the userspace implementation")
	}
	if len(s.Address) == 0 || len(s.Peers) == 0 {
		return WGIface{}, fmt.Errorf("settings.address and settings.peers are required")
	}
	iface := WGIface{NodeID: n.ID, Name: WGIfaceName(n.ID), PrivateKey: n.Credential, MTU: s.MTU}
	if iface.MTU <= 0 {
		iface.MTU = WGDefaultMTU
	}
	for _, a := range s.Address {
		if !strings.Contains(a, "/") {
			if strings.Contains(a, ":") {
				a += "/128"
			} else {
				a += "/32"
			}
		}
		iface.Addresses = append(iface.Addresses, a)
	}
	for _, p := range s.Peers {
		if p.PublicKey == "" {
			return WGIface{}, fmt.Errorf("peer without publicKey")
		}
		ep := p.Endpoint
		if ep == "" {
			ep = fmt.Sprintf("%s:%d", n.Address, n.Port)
		}
		ka := p.KeepAlive
		if ka <= 0 {
			ka = WGKeepalive
		}
		allowed := p.AllowedIPs
		if len(allowed) == 0 {
			allowed = []string{"0.0.0.0/0", "::/0"}
		}
		iface.Peers = append(iface.Peers, WGPeer{PublicKey: p.PublicKey, PresharedKey: p.PreSharedKey, Endpoint: ep, AllowedIPs: allowed, Keepalive: ka})
	}
	return iface, nil
}

// renderWG fills a.WG (sorted by node id; table and mark follow the index)
// and reports nodes that fall back to userspace in a.Warnings.
func renderWG(snap *desired.Snapshot, d Deps, a *Artifacts) {
	if !d.KernelWG {
		return
	}
	var ifaces []WGIface
	for _, n := range snap.EnabledNodes() {
		if strings.ToLower(string(n.Protocol)) != "wireguard" {
			continue
		}
		iface, err := kernelWGIface(n)
		if err != nil {
			a.warn("node %q stays on userspace WireGuard: %v", n.Name, err)
			continue
		}
		ifaces = append(ifaces, iface)
	}
	sort.Slice(ifaces, func(i, j int) bool { return ifaces[i].NodeID < ifaces[j].NodeID })
	if len(ifaces) > WGMaxIfaces {
		a.warn("only the first %d WireGuard nodes use the kernel implementation", WGMaxIfaces)
		ifaces = ifaces[:WGMaxIfaces]
	}
	for i := range ifaces {
		ifaces[i].Table = WGTableBase + i
		ifaces[i].Mark = WGMarkBase + i
	}
	a.WG = ifaces
}

// wgOutbound is the Xray outbound for a node served by a kernel interface.
func wgOutbound(nodeID string, iface WGIface) map[string]any {
	return map[string]any{
		"tag": "node-" + nodeID, "protocol": "freedom",
		"settings":       map[string]any{"domainStrategy": "AsIs"},
		"streamSettings": map[string]any{"sockopt": map[string]any{"mark": iface.Mark}},
	}
}
