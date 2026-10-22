package observe

import (
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func dnsAnswerPacket(t *testing.T, src, dst [4]byte, name string, ips ...string) []byte {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	m.Response = true
	for _, ip := range ips {
		m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP(ip).To4()})
	}
	payload, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	udp := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(udp[0:2], 53)
	binary.BigEndian.PutUint16(udp[2:4], 40000)
	binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp)))
	copy(udp[8:], payload)
	ip := make([]byte, 20+len(udp))
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(len(ip)))
	ip[8] = 64
	ip[9] = 17
	copy(ip[12:16], src[:])
	copy(ip[16:20], dst[:])
	copy(ip[20:], udp)
	return ip
}

func TestDNSTapRecordsAnswersPerClient(t *testing.T) {
	tap := NewDNSTap(slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Now()
	tap.handle(dnsAnswerPacket(t, [4]byte{192, 168, 1, 1}, [4]byte{192, 168, 1, 50}, "claude.ai", "104.18.32.47", "104.18.33.47"), now)
	tap.handle(dnsAnswerPacket(t, [4]byte{192, 168, 1, 1}, [4]byte{192, 168, 1, 60}, "www.cloudflare.com", "104.18.32.47"), now)
	if got := tap.Lookup("192.168.1.50:51234", "104.18.33.47:443"); got != "claude.ai" {
		t.Fatalf("client-specific lookup: %q", got)
	}
	if got := tap.Lookup("192.168.1.60", "104.18.32.47:443"); got != "www.cloudflare.com" {
		t.Fatalf("second client sees its own answer: %q", got)
	}
	// Unknown client falls back to the most recent answer for the address.
	if got := tap.Lookup("192.168.1.99", "104.18.32.47"); got != "www.cloudflare.com" {
		t.Fatalf("global fallback: %q", got)
	}
	if got := tap.Lookup("192.168.1.50", "example.com:443"); got != "" {
		t.Fatalf("names are not looked up: %q", got)
	}
	if tap.Seen() != 2 {
		t.Fatalf("seen %d", tap.Seen())
	}
	// Not an answer / not UDP 53: ignored.
	pkt := dnsAnswerPacket(t, [4]byte{1, 1, 1, 1}, [4]byte{2, 2, 2, 2}, "x.example", "9.9.9.9")
	binary.BigEndian.PutUint16(pkt[20:22], 5353)
	tap.handle(pkt, now)
	if tap.Seen() != 2 {
		t.Fatal("source port other than 53 must be ignored")
	}
	// Expiry.
	tap.TTL = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	if got := tap.Lookup("192.168.1.50", "104.18.33.47"); got != "" {
		t.Fatalf("expired entry returned: %q", got)
	}
	// Record with a client address: Max cap is respected.
	tap.TTL = time.Hour
	tap.Max = tap.count // full: nothing new may be added
	tap.Record(netip.MustParseAddr("10.0.0.1"), "b.example", []netip.Addr{netip.MustParseAddr("2.2.2.2")}, now)
	if got := tap.Lookup("10.0.0.1", "2.2.2.2"); got != "" || tap.count != tap.Max {
		t.Fatalf("cap not enforced: %q count=%d max=%d", got, tap.count, tap.Max)
	}
}
