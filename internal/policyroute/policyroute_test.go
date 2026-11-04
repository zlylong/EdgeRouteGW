package policyroute

import (
	"syscall"
	"testing"

	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

func msg(typ uint16, table uint8, attrTable int) syscall.NetlinkMessage {
	hdr := nl.RtMsg{}
	hdr.Family = unix.AF_INET
	hdr.Table = table
	data := hdr.Serialize()
	if attrTable > 0 {
		data = append(data, nl.NewRtAttr(unix.RTA_TABLE, nl.Uint32Attr(uint32(attrTable))).Serialize()...)
	}
	return syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: typ}, Data: data}
}

func TestInteresting(t *testing.T) {
	watched := map[int]bool{100: true, 300: true, 1000: true}
	cases := []struct {
		name string
		m    syscall.NetlinkMessage
		want bool
	}{
		{"rule deleted in tproxy table", msg(unix.RTM_DELRULE, 100, 0), true},
		{"rule deleted in wg table (id over a byte)", msg(unix.RTM_DELRULE, unix.RT_TABLE_COMPAT, 300), true},
		{"rule deleted in main", msg(unix.RTM_DELRULE, unix.RT_TABLE_MAIN, 0), false},
		{"route deleted in tproxy table", msg(unix.RTM_DELROUTE, 100, 0), true},
		{"route deleted in main (dhcp renew)", msg(unix.RTM_DELROUTE, unix.RT_TABLE_MAIN, 0), false},
		{"route added", msg(unix.RTM_NEWROUTE, 100, 0), false},
		{"rule added", msg(unix.RTM_NEWRULE, 100, 0), false},
		{"large table id via attribute", msg(unix.RTM_DELROUTE, unix.RT_TABLE_COMPAT, 1000), true},
		{"large table id not watched", msg(unix.RTM_DELROUTE, unix.RT_TABLE_COMPAT, 1001), false},
		{"truncated", syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: unix.RTM_DELRULE}, Data: []byte{1, 2}}, false},
	}
	for _, c := range cases {
		if got := Interesting(c.m, watched); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
