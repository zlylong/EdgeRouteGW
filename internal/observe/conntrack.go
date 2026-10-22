// Package observe collects runtime telemetry that is not desired state: the
// connection log Xray writes, and later traffic counters.
package observe

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/events"
)

// Connection is one parsed Xray access-log line.
type Connection struct {
	At       time.Time `json:"at"`
	Source   string    `json:"source"`   // client ip:port
	Network  string    `json:"network"`  // tcp/udp
	Target   string    `json:"target"`   // host:port as Xray saw it (domain when sniffed)
	Inbound  string    `json:"inbound"`  // inbound tag
	Outbound string    `json:"outbound"` // outbound or balancer tag
	Accepted bool      `json:"accepted"`
	// Geo lists the geoip/geosite tags the target belongs to ("geoip:cn"),
	// so an operator can see which rule would catch it.
	Geo []string `json:"geo,omitempty"`
	// Domain is the name the client most recently resolved to the target
	// address (from the DNS tap); empty when unknown or when the target is
	// already a name.
	Domain string `json:"domain,omitempty"`
}

// EventConnection is the bus type for new connections.
const EventConnection events.Type = "connection"

// Xray access log: "2026/10/20 08:51:58.123456 from 192.168.1.10:51234 accepted tcp:www.google.com:443 [tproxy_in -> node-abc] email: x"
// or with balancer: "[tproxy_in >> group-x]". Rejected lines read "rejected".
var lineRe = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?) from (\S+) (accepted|rejected) (tcp|udp):(\S+) \[([^\]]+)\]`)

// ConnTracker tails the access log into a ring buffer.
type ConnTracker struct {
	Path string
	Bus  *events.Bus
	Log  *slog.Logger
	// Capacity is the ring size.
	Capacity int
	// Enrich tags a target ("host:port") with geo labels; nil disables it.
	Enrich func(target string) []string
	// DomainHint returns the domain a client (ip:port) resolved to the
	// target address; nil disables it.
	DomainHint func(client, target string) string

	mu   sync.RWMutex
	ring []Connection
	next int
	full bool
}

// NewConnTracker builds a tracker for path.
func NewConnTracker(path string, bus *events.Bus, log *slog.Logger) *ConnTracker {
	return &ConnTracker{Path: path, Bus: bus, Log: log, Capacity: 2000}
}

// Recent returns up to limit newest-first connections, optionally filtered
// by a substring on source, target or outbound.
func (t *ConnTracker) Recent(limit int, q string) []Connection {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := len(t.ring)
	if limit <= 0 || limit > n {
		limit = n
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]Connection, 0, limit)
	idx := t.next - 1
	for i := 0; i < n && len(out) < limit; i++ {
		if idx < 0 {
			idx = n - 1
		}
		c := t.ring[idx]
		idx--
		if q != "" && !strings.Contains(strings.ToLower(c.Source+" "+c.Target+" "+c.Domain+" "+c.Outbound), q) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (t *ConnTracker) add(c Connection) {
	if t.DomainHint != nil && c.Domain == "" {
		c.Domain = t.DomainHint(c.Source, c.Target)
	}
	if t.Enrich != nil && c.Geo == nil {
		c.Geo = t.Enrich(c.Target)
		if c.Domain != "" {
			for _, g := range t.Enrich(c.Domain + ":0") {
				if !slices.Contains(c.Geo, g) {
					c.Geo = append(c.Geo, g)
				}
			}
		}
	}
	t.mu.Lock()
	if t.Capacity <= 0 {
		t.Capacity = 2000
	}
	if len(t.ring) < t.Capacity {
		t.ring = append(t.ring, c)
		t.next = len(t.ring) % t.Capacity
	} else {
		t.ring[t.next] = c
		t.next = (t.next + 1) % t.Capacity
		t.full = true
	}
	t.mu.Unlock()
	if t.Bus != nil {
		t.Bus.Publish(EventConnection, c)
	}
}

// Parse decodes one access-log line.
func Parse(line string) (Connection, bool) {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return Connection{}, false
	}
	at, err := time.ParseInLocation("2006/01/02 15:04:05.000000", m[1], time.Local)
	if err != nil {
		if at, err = time.ParseInLocation("2006/01/02 15:04:05", m[1], time.Local); err != nil {
			at = time.Now()
		}
	}
	c := Connection{At: at.UTC(), Source: m[2], Accepted: m[3] == "accepted", Network: m[4], Target: m[5]}
	route := m[6]
	sep := " -> "
	if strings.Contains(route, " >> ") {
		sep = " >> "
	}
	in, out, _ := strings.Cut(route, sep)
	c.Inbound, c.Outbound = strings.TrimSpace(in), strings.TrimSpace(out)
	return c, true
}

// Run tails the file until ctx ends. It starts at the end of the current
// file (history is not replayed), reopens when the file is truncated or
// replaced, and waits for the file to appear if it does not exist yet.
func (t *ConnTracker) Run(ctx context.Context) error {
	var f *os.File
	var rd *bufio.Reader
	var offset int64
	var inode uint64
	reopen := func() bool {
		if f != nil {
			f.Close()
			f = nil
		}
		nf, err := os.Open(t.Path)
		if err != nil {
			return false
		}
		st, err := nf.Stat()
		if err != nil {
			nf.Close()
			return false
		}
		offset = st.Size()
		if _, err := nf.Seek(offset, io.SeekStart); err != nil {
			nf.Close()
			return false
		}
		f, rd, inode = nf, bufio.NewReaderSize(nf, 64<<10), fileInode(st)
		return true
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if f == nil && !reopen() {
			continue
		}
		st, err := os.Stat(t.Path)
		if err != nil || fileInode(st) != inode || st.Size() < offset {
			// Rotated or truncated: start from the beginning of the new file.
			if f != nil {
				f.Close()
				f = nil
			}
			if nf, err := os.Open(t.Path); err == nil {
				if st2, err := nf.Stat(); err == nil {
					f, rd, inode, offset = nf, bufio.NewReaderSize(nf, 64<<10), fileInode(st2), 0
				} else {
					nf.Close()
				}
			}
			if f == nil {
				continue
			}
		}
		for {
			line, err := rd.ReadString('\n')
			if len(line) > 0 && err == nil {
				offset += int64(len(line))
				if c, ok := Parse(strings.TrimRight(line, "\r\n")); ok {
					t.add(c)
				}
				continue
			}
			if len(line) > 0 && err == io.EOF {
				// Partial line: rewind so it is read whole next time.
				_, _ = f.Seek(offset, io.SeekStart)
				rd.Reset(f)
			}
			break
		}
	}
}
