// Package policyroute checks and watches the kernel policy routing the
// gateway depends on: the "ip rule fwmark N lookup T" entries and the routes
// in those tables. The entries are installed once by the drivers, but other
// agents on the host remove them: systemd-networkd deletes every rule it did
// not configure when it (re)starts (ManageForeignRoutingPolicyRules=yes is
// the default), and a restart triggered by unattended-upgrades took a
// production gateway offline for 21 minutes while the control plane kept
// reporting "active". Drivers call the helpers here from Plan/Health so the
// periodic resync repairs drift, and Watch turns a deletion into an
// immediate reconcile pass instead of a wait for the next resync.
package policyroute

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

// Family names for messages.
func famName(fam int) string {
	if fam == netlink.FAMILY_V6 {
		return "-6"
	}
	return "-4"
}

// IPv6Disabled reports whether the host has IPv6 switched off entirely, in
// which case no v6 entries are expected.
func IPv6Disabled() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// Families returns the address families entries are expected in.
func Families() []int {
	if IPv6Disabled() {
		return []int{netlink.FAMILY_V4}
	}
	return []int{netlink.FAMILY_V4, netlink.FAMILY_V6}
}

// HasRule reports whether an "ip rule fwmark <mark> lookup <table>" entry
// exists in the family.
func HasRule(fam int, mark uint32, table int) (bool, error) {
	rules, err := netlink.RuleList(fam)
	if err != nil {
		return false, fmt.Errorf("ip %s rule list: %w", famName(fam), err)
	}
	for _, r := range rules {
		if r.Mark == mark && r.Table == table {
			return true, nil
		}
	}
	return false, nil
}

func isDefault(r netlink.Route) bool {
	if r.Dst == nil || r.Dst.IP == nil {
		return true
	}
	ones, _ := r.Dst.Mask.Size()
	return ones == 0
}

// HasLocalDefault reports whether table holds "local default dev lo" (the
// route that delivers TPROXY-marked packets to the local socket).
func HasLocalDefault(fam int, table int) (bool, error) {
	routes, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return false, fmt.Errorf("ip %s route list table %d: %w", famName(fam), table, err)
	}
	for _, r := range routes {
		if r.Type == unix.RTN_LOCAL && isDefault(r) {
			return true, nil
		}
	}
	return false, nil
}

// HasDefaultVia reports whether table holds a default route through the
// link (the WireGuard table's "default dev ergw-xxxx").
func HasDefaultVia(fam int, table int, linkIndex int) (bool, error) {
	routes, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return false, fmt.Errorf("ip %s route list table %d: %w", famName(fam), table, err)
	}
	for _, r := range routes {
		if r.LinkIndex == linkIndex && isDefault(r) {
			return true, nil
		}
	}
	return false, nil
}

// TProxyMissing lists the policy-routing entries for the TPROXY path that
// are absent: the fwmark rule and the local default route, per family.
func TProxyMissing(mark uint32, table int) ([]string, error) {
	var missing []string
	for _, fam := range Families() {
		ok, err := HasRule(fam, mark, table)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, fmt.Sprintf("ip %s rule fwmark %#x lookup %d", famName(fam), mark, table))
		}
		ok, err = HasLocalDefault(fam, table)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, fmt.Sprintf("ip %s route local default dev lo table %d", famName(fam), table))
		}
	}
	return missing, nil
}

// --- watcher ---------------------------------------------------------------

// Debounce is how long the watcher waits after the last interesting
// deletion before triggering; networkd removes rules one message at a time.
const Debounce = 750 * time.Millisecond

// Watch subscribes to rule and route changes and calls trigger (debounced)
// whenever an entry in one of the tables is deleted. It returns when ctx is
// done. When the subscription cannot be set up it logs and returns nil: the
// periodic resync still repairs drift, only later.
func Watch(ctx context.Context, tables []int, trigger func(), log *slog.Logger) error {
	s, err := nl.Subscribe(unix.NETLINK_ROUTE, unix.RTNLGRP_IPV4_RULE, unix.RTNLGRP_IPV6_RULE, unix.RTNLGRP_IPV4_ROUTE, unix.RTNLGRP_IPV6_ROUTE)
	if err != nil {
		log.Warn("policy routing watch unavailable; drift is repaired by the periodic resync only", "err", err)
		return nil
	}
	defer s.Close()
	_ = s.SetReceiveTimeout(&unix.Timeval{Sec: 1})
	watched := make(map[int]bool, len(tables))
	for _, t := range tables {
		watched[t] = true
	}
	log.Info("watching policy routing", "tables", tables)

	var mu sync.Mutex
	var timer *time.Timer
	fire := func() {
		if ctx.Err() != nil {
			return
		}
		log.Info("policy routing entry removed externally; reconciling")
		trigger()
	}
	arm := func() {
		mu.Lock()
		defer mu.Unlock()
		if timer == nil {
			timer = time.AfterFunc(Debounce, fire)
		} else {
			timer.Reset(Debounce)
		}
	}
	defer func() {
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}
		msgs, _, err := s.Receive()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			if errors.Is(err, unix.ENOBUFS) {
				// The kernel dropped notifications; assume something changed.
				log.Debug("policy routing watch overflow", "err", err)
				arm()
				continue
			}
			log.Warn("policy routing watch stopped; drift is repaired by the periodic resync only", "err", err)
			return nil
		}
		for _, m := range msgs {
			if Interesting(m, watched) {
				log.Debug("policy routing deletion seen", "type", m.Header.Type, "table", messageTable(m))
				arm()
			}
		}
	}
}

// Interesting reports whether a netlink message is the deletion of a rule or
// route in one of the watched tables.
func Interesting(m syscall.NetlinkMessage, watched map[int]bool) bool {
	switch m.Header.Type {
	case unix.RTM_DELRULE, unix.RTM_DELROUTE:
	default:
		return false
	}
	return watched[messageTable(m)]
}

// messageTable extracts the table id from a rule or route message: the
// header byte, or the RTA_TABLE / FRA_TABLE attribute (both 15) when the
// header says RT_TABLE_COMPAT because the id does not fit in a byte. The
// fib_rule_hdr and rtmsg structs share their layout for the fields used.
func messageTable(m syscall.NetlinkMessage) int {
	if len(m.Data) < unix.SizeofRtMsg {
		return -1
	}
	hdr := nl.DeserializeRtMsg(m.Data)
	table := int(hdr.Table)
	if hdr.Table != unix.RT_TABLE_COMPAT {
		return table
	}
	attrs, err := nl.ParseRouteAttr(m.Data[unix.SizeofRtMsg:])
	if err != nil {
		return table
	}
	for _, a := range attrs {
		if a.Attr.Type == unix.RTA_TABLE && len(a.Value) >= 4 {
			return int(binary.NativeEndian.Uint32(a.Value))
		}
	}
	return table
}
