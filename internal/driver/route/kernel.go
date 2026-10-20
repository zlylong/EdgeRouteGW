package route

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Kernel is the routing-table surface the driver needs. Netlink implements
// it for real; Fake stands in for tests.
type Kernel interface {
	// List returns the prefixes in table owned by proto.
	List(table, proto int) ([]netip.Prefix, error)
	// Add installs prefixes into table (dev lo, proto). Existing entries are
	// replaced.
	Add(table, proto int, prefixes []netip.Prefix) error
	// Del removes prefixes from table.
	Del(table, proto int, prefixes []netip.Prefix) error
}

// Netlink talks to the kernel.
type Netlink struct {
	loIndex int
	once    sync.Once
	loErr   error
}

func (n *Netlink) lo() (int, error) {
	n.once.Do(func() {
		link, err := netlink.LinkByName("lo")
		if err != nil {
			n.loErr = err
			return
		}
		n.loIndex = link.Attrs().Index
	})
	return n.loIndex, n.loErr
}

// List implements Kernel.
func (n *Netlink) List(table, proto int) ([]netip.Prefix, error) {
	filter := &netlink.Route{Table: table, Protocol: netlink.RouteProtocol(proto)}
	rs, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		return nil, fmt.Errorf("list routes table %d: %w", table, err)
	}
	out := make([]netip.Prefix, 0, len(rs))
	for _, r := range rs {
		if r.Dst == nil {
			continue
		}
		ones, _ := r.Dst.Mask.Size()
		if a, ok := netip.AddrFromSlice(r.Dst.IP.To4()); ok {
			out = append(out, netip.PrefixFrom(a, ones))
		}
	}
	sortPrefixes(out)
	return out, nil
}

func (n *Netlink) route(table, proto int, p netip.Prefix) (*netlink.Route, error) {
	idx, err := n.lo()
	if err != nil {
		return nil, err
	}
	return &netlink.Route{
		Dst:       &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), 32)},
		LinkIndex: idx,
		Table:     table,
		Protocol:  netlink.RouteProtocol(proto),
		Scope:     netlink.SCOPE_LINK,
		Type:      unix.RTN_UNICAST,
	}, nil
}

// Add implements Kernel.
func (n *Netlink) Add(table, proto int, prefixes []netip.Prefix) error {
	for _, p := range prefixes {
		r, err := n.route(table, proto, p)
		if err != nil {
			return err
		}
		if err := netlink.RouteReplace(r); err != nil {
			return fmt.Errorf("add %s: %w", p, err)
		}
	}
	return nil
}

// Del implements Kernel.
func (n *Netlink) Del(table, proto int, prefixes []netip.Prefix) error {
	for _, p := range prefixes {
		r, err := n.route(table, proto, p)
		if err != nil {
			return err
		}
		if err := netlink.RouteDel(r); err != nil && !isNotExist(err) {
			return fmt.Errorf("del %s: %w", p, err)
		}
	}
	return nil
}

func isNotExist(err error) bool {
	return err == unix.ESRCH || err == unix.ENOENT || err.Error() == "no such process"
}

func sortPrefixes(ps []netip.Prefix) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Addr() != ps[j].Addr() {
			return ps[i].Addr().Less(ps[j].Addr())
		}
		return ps[i].Bits() < ps[j].Bits()
	})
}

// Fake is an in-memory Kernel.
type Fake struct {
	mu     sync.Mutex
	Tables map[int]map[netip.Prefix]bool
	Adds   int
	Dels   int
	Fail   error
}

// NewFake creates an empty fake.
func NewFake() *Fake { return &Fake{Tables: map[int]map[netip.Prefix]bool{}} }

// List implements Kernel.
func (f *Fake) List(table, _ int) ([]netip.Prefix, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return nil, f.Fail
	}
	out := []netip.Prefix{}
	for p := range f.Tables[table] {
		out = append(out, p)
	}
	sortPrefixes(out)
	return out, nil
}

// Add implements Kernel.
func (f *Fake) Add(table, _ int, prefixes []netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	if f.Tables[table] == nil {
		f.Tables[table] = map[netip.Prefix]bool{}
	}
	for _, p := range prefixes {
		f.Tables[table][p] = true
		f.Adds++
	}
	return nil
}

// Del implements Kernel.
func (f *Fake) Del(table, _ int, prefixes []netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	for _, p := range prefixes {
		delete(f.Tables[table], p)
		f.Dels++
	}
	return nil
}
