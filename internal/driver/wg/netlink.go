package wg

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// Netlink is the production backend: rtnetlink for links, addresses,
// routes and rules; the WireGuard generic netlink family for keys and
// peers (no wireguard-tools needed).
type Netlink struct{}

// Ensure implements Backend.
func (Netlink) Ensure(w render.WGIface) error {
	link, err := netlink.LinkByName(w.Name)
	if err != nil {
		if err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: w.Name, MTU: w.MTU}}); err != nil {
			return fmt.Errorf("link add: %w", err)
		}
		if link, err = netlink.LinkByName(w.Name); err != nil {
			return err
		}
	}
	if link.Attrs().MTU != w.MTU {
		if err := netlink.LinkSetMTU(link, w.MTU); err != nil {
			return fmt.Errorf("mtu: %w", err)
		}
	}
	cfg, err := wgConfig(w)
	if err != nil {
		return err
	}
	c, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("wgctrl: %w", err)
	}
	defer c.Close()
	if err := c.ConfigureDevice(w.Name, cfg); err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	// Addresses: add the wanted ones, drop the rest.
	want := map[string]bool{}
	for _, a := range w.Addresses {
		want[a] = true
	}
	addrs, _ := netlink.AddrList(link, netlink.FAMILY_ALL)
	for _, a := range addrs {
		if a.Scope == unix.RT_SCOPE_LINK && a.IP.To4() == nil {
			continue // IPv6 link-local, kernel managed
		}
		if !want[a.IPNet.String()] {
			_ = netlink.AddrDel(link, &a)
		}
	}
	for _, a := range w.Addresses {
		addr, err := netlink.ParseAddr(a)
		if err != nil {
			return fmt.Errorf("address %q: %w", a, err)
		}
		if err := netlink.AddrReplace(link, addr); err != nil {
			return fmt.Errorf("address %s: %w", a, err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("up: %w", err)
	}
	// Policy routing: default through the tunnel in its own table, selected
	// by the mark Xray puts on the outbound sockets.
	for _, fam := range families(w) {
		dst := "0.0.0.0/0"
		if fam == netlink.FAMILY_V6 {
			dst = "::/0"
		}
		_, dstNet, _ := net.ParseCIDR(dst)
		if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: dstNet, Table: w.Table, Protocol: render.RouteProtocol}); err != nil {
			return fmt.Errorf("table %d default: %w", w.Table, err)
		}
		if err := ensureRule(fam, w.Mark, w.Table); err != nil {
			return err
		}
	}
	return nil
}

func families(w render.WGIface) []int {
	v4, v6 := false, false
	for _, a := range w.Addresses {
		if p, err := netip.ParsePrefix(a); err == nil {
			if p.Addr().Is4() {
				v4 = true
			} else {
				v6 = true
			}
		}
	}
	var out []int
	if v4 {
		out = append(out, netlink.FAMILY_V4)
	}
	if v6 {
		out = append(out, netlink.FAMILY_V6)
	}
	return out
}

func ensureRule(fam, mark, table int) error {
	rules, err := netlink.RuleList(fam)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if r.Mark == uint32(mark) && r.Table == table {
			return nil
		}
	}
	r := netlink.NewRule()
	r.Family = fam
	r.Mark = uint32(mark)
	r.Mask = func() *uint32 { m := uint32(0xffffffff); return &m }()
	r.Table = table
	r.Priority = 20000 + table
	if err := netlink.RuleAdd(r); err != nil && !strings.Contains(err.Error(), "exists") {
		return fmt.Errorf("ip rule fwmark %#x lookup %d: %w", mark, table, err)
	}
	return nil
}

func wgConfig(w render.WGIface) (wgtypes.Config, error) {
	priv, err := wgtypes.ParseKey(w.PrivateKey)
	if err != nil {
		return wgtypes.Config{}, fmt.Errorf("private key: %w", err)
	}
	cfg := wgtypes.Config{PrivateKey: &priv, ReplacePeers: true}
	for _, p := range w.Peers {
		pub, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			return wgtypes.Config{}, fmt.Errorf("peer public key: %w", err)
		}
		pc := wgtypes.PeerConfig{PublicKey: pub, ReplaceAllowedIPs: true}
		if p.PresharedKey != "" {
			psk, err := wgtypes.ParseKey(p.PresharedKey)
			if err != nil {
				return wgtypes.Config{}, fmt.Errorf("preshared key: %w", err)
			}
			pc.PresharedKey = &psk
		}
		if p.Endpoint != "" {
			ep, err := net.ResolveUDPAddr("udp", p.Endpoint)
			if err != nil {
				return wgtypes.Config{}, fmt.Errorf("endpoint %q: %w", p.Endpoint, err)
			}
			pc.Endpoint = ep
		}
		if p.Keepalive > 0 {
			ka := time.Duration(p.Keepalive) * time.Second
			pc.PersistentKeepaliveInterval = &ka
		}
		for _, a := range p.AllowedIPs {
			_, n, err := net.ParseCIDR(a)
			if err != nil {
				return wgtypes.Config{}, fmt.Errorf("allowed ip %q: %w", a, err)
			}
			pc.AllowedIPs = append(pc.AllowedIPs, *n)
		}
		cfg.Peers = append(cfg.Peers, pc)
	}
	return cfg, nil
}

// Remove implements Backend.
func (Netlink) Remove(name string, table, mark int) error {
	for _, fam := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		if rules, err := netlink.RuleList(fam); err == nil {
			for _, r := range rules {
				if (mark != 0 && r.Mark == uint32(mark)) || (table != 0 && r.Table == table && r.Table >= render.WGTableBase && r.Table < render.WGTableBase+render.WGMaxIfaces) {
					_ = netlink.RuleDel(&r)
				}
			}
		}
		if table != 0 {
			if routes, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: table}, netlink.RT_FILTER_TABLE); err == nil {
				for _, rt := range routes {
					_ = netlink.RouteDel(&rt)
				}
			}
		}
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil // already gone
	}
	return netlink.LinkDel(link)
}

// List implements Backend.
func (Netlink) List(prefix string) ([]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range links {
		if l.Type() == "wireguard" && strings.HasPrefix(l.Attrs().Name, prefix) {
			out = append(out, l.Attrs().Name)
		}
	}
	return out, nil
}

// Peers implements Backend.
func (Netlink) Peers(name string) ([]PeerState, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	dev, err := c.Device(name)
	if err != nil {
		return nil, err
	}
	out := make([]PeerState, 0, len(dev.Peers))
	for _, p := range dev.Peers {
		out = append(out, PeerState{LastHandshake: p.LastHandshakeTime, RxBytes: p.ReceiveBytes, TxBytes: p.TransmitBytes})
	}
	return out, nil
}
