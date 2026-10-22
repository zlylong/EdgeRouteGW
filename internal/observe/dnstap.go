package observe

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/florianl/go-nflog/v2"
	"github.com/miekg/dns"

	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// DNSTap remembers which domain each LAN client resolved to which address,
// so a connection seen as "10.0.0.5 -> 104.18.32.47:443" can be labelled
// with the domain behind it. The data comes from copies of the answers
// Mosdns sends to clients (an nftables "log group" rule on the output
// hook, delivered over nflog); nothing sits in the DNS path and routing is
// untouched. Only UDP answers are seen, which covers nearly all client
// traffic.
type DNSTap struct {
	Log   *slog.Logger
	Group uint16
	// TTL bounds how long a mapping is kept; Max bounds the total entries.
	TTL time.Duration
	Max int

	mu       sync.RWMutex
	byClient map[netip.Addr]map[netip.Addr]hint // client -> answer ip -> name
	global   map[netip.Addr]hint                // answer ip -> name (any client)
	count    int
	seen     uint64
}

type hint struct {
	name string
	at   time.Time
}

// NewDNSTap returns a tap on the default nflog group.
func NewDNSTap(log *slog.Logger) *DNSTap {
	return &DNSTap{Log: log, Group: render.DNSTapGroup, TTL: 30 * time.Minute, Max: 100000}
}

// Lookup returns the domain client most recently resolved to the address in
// target ("ip:port" or "ip"), falling back to any client's answer; "" when
// unknown or when target is already a name.
func (t *DNSTap) Lookup(client, target string) string {
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return ""
	}
	ip = ip.Unmap()
	var cip netip.Addr
	if h, _, err := net.SplitHostPort(client); err == nil {
		client = h
	}
	if a, err := netip.ParseAddr(client); err == nil {
		cip = a.Unmap()
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if m := t.byClient[cip]; m != nil {
		if h, ok := m[ip]; ok && time.Since(h.at) < t.TTL {
			return h.name
		}
	}
	if h, ok := t.global[ip]; ok && time.Since(h.at) < t.TTL {
		return h.name
	}
	return ""
}

// Seen reports how many answers the tap has recorded (diagnostics).
func (t *DNSTap) Seen() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.seen
}

// Record stores one answer. Exported for tests and for other sources.
func (t *DNSTap) Record(client netip.Addr, name string, answers []netip.Addr, now time.Time) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" || len(answers) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byClient == nil {
		t.byClient = map[netip.Addr]map[netip.Addr]hint{}
		t.global = map[netip.Addr]hint{}
	}
	if t.count >= t.Max {
		t.pruneLocked(now)
		if t.count >= t.Max {
			return // still full: drop rather than grow without bound
		}
	}
	m := t.byClient[client]
	if m == nil {
		m = map[netip.Addr]hint{}
		t.byClient[client] = m
	}
	for _, a := range answers {
		a = a.Unmap()
		if _, ok := m[a]; !ok {
			t.count++
		}
		m[a] = hint{name: name, at: now}
		t.global[a] = hint{name: name, at: now}
	}
	t.seen++
}

func (t *DNSTap) pruneLocked(now time.Time) {
	for c, m := range t.byClient {
		for a, h := range m {
			if now.Sub(h.at) > t.TTL {
				delete(m, a)
				t.count--
			}
		}
		if len(m) == 0 {
			delete(t.byClient, c)
		}
	}
	for a, h := range t.global {
		if now.Sub(h.at) > t.TTL {
			delete(t.global, a)
		}
	}
}

// Run receives packets from the nflog group until ctx ends. The group only
// carries traffic once the nftables rule exists, so a failure to open it
// is retried rather than fatal.
func (t *DNSTap) Run(ctx context.Context) error {
	prune := time.NewTicker(5 * time.Minute)
	defer prune.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-prune.C:
				t.mu.Lock()
				t.pruneLocked(time.Now())
				t.mu.Unlock()
			}
		}
	}()
	for {
		err := t.listen(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			t.Log.Warn("dns tap unavailable; connection domains will be missing", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(30 * time.Second):
		}
	}
}

func (t *DNSTap) listen(ctx context.Context) error {
	nf, err := nflog.Open(&nflog.Config{Group: t.Group, Copymode: nflog.CopyPacket, Bufsize: 2048, Timeout: 10})
	if err != nil {
		return err
	}
	defer nf.Close()
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	hook := func(a nflog.Attribute) int {
		if a.Payload != nil {
			t.handle(*a.Payload, time.Now())
		}
		return 0
	}
	errFn := func(e error) int {
		select {
		case errCh <- e:
		default:
		}
		return 1
	}
	if err := nf.RegisterWithErrorFunc(lctx, hook, errFn); err != nil {
		return err
	}
	t.Log.Info("dns tap listening", "nflog_group", t.Group)
	select {
	case <-ctx.Done():
		return nil
	case e := <-errCh:
		return e
	}
}

// handle parses one IP packet (no link-layer header) carrying a UDP DNS
// answer and records its A/AAAA records under the client (the packet's
// destination).
func (t *DNSTap) handle(pkt []byte, now time.Time) {
	client, payload, ok := udpPayload(pkt)
	if !ok {
		return
	}
	var msg dns.Msg
	if err := msg.Unpack(payload); err != nil || !msg.Response || len(msg.Question) == 0 {
		return
	}
	name := msg.Question[0].Name
	var answers []netip.Addr
	for _, rr := range msg.Answer {
		switch r := rr.(type) {
		case *dns.A:
			if a, ok := netip.AddrFromSlice(r.A.To4()); ok {
				answers = append(answers, a)
			}
		case *dns.AAAA:
			if a, ok := netip.AddrFromSlice(r.AAAA); ok {
				answers = append(answers, a)
			}
		}
	}
	if len(answers) > 0 {
		t.Record(client, name, answers, now)
	}
}

var errNotDNS = errors.New("not a udp dns answer")

// udpPayload returns the destination address and the UDP payload of an
// IPv4/IPv6 packet whose source port is 53.
func udpPayload(pkt []byte) (netip.Addr, []byte, bool) {
	if len(pkt) < 20 {
		return netip.Addr{}, nil, false
	}
	var dst netip.Addr
	var off int
	switch pkt[0] >> 4 {
	case 4:
		ihl := int(pkt[0]&0x0f) * 4
		if ihl < 20 || len(pkt) < ihl+8 || pkt[9] != 17 {
			return netip.Addr{}, nil, false
		}
		dst = netip.AddrFrom4([4]byte(pkt[16:20]))
		off = ihl
	case 6:
		if len(pkt) < 48 || pkt[6] != 17 {
			return netip.Addr{}, nil, false
		}
		dst = netip.AddrFrom16([16]byte(pkt[24:40])).Unmap()
		off = 40
	default:
		return netip.Addr{}, nil, false
	}
	if binary.BigEndian.Uint16(pkt[off:off+2]) != 53 {
		return netip.Addr{}, nil, false
	}
	ulen := int(binary.BigEndian.Uint16(pkt[off+4 : off+6]))
	if ulen < 8 || off+ulen > len(pkt) {
		ulen = len(pkt) - off
	}
	return dst, pkt[off+8 : off+ulen], true
}
