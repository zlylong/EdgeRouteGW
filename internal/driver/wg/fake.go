package wg

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// Fake records what the driver asked for (tests).
type Fake struct {
	mu     sync.Mutex
	Ifaces map[string]render.WGIface
	// Handshake controls Peers(): zero means never.
	Handshake map[string]time.Time
	Calls     []string
	FailOn    string
}

// NewFake returns an empty backend.
func NewFake() *Fake {
	return &Fake{Ifaces: map[string]render.WGIface{}, Handshake: map[string]time.Time{}}
}

func (f *Fake) Ensure(w render.WGIface) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "ensure "+w.Name)
	if f.FailOn == w.Name {
		return fmt.Errorf("simulated failure")
	}
	f.Ifaces[w.Name] = w
	return nil
}

func (f *Fake) Remove(name string, table, mark int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "remove "+name)
	delete(f.Ifaces, name)
	return nil
}

func (f *Fake) List(prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.Ifaces {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func (f *Fake) Peers(name string) ([]PeerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Ifaces[name]; !ok {
		return nil, fmt.Errorf("no such device")
	}
	return []PeerState{{LastHandshake: f.Handshake[name]}}, nil
}
