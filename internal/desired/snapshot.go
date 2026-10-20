// Package desired loads one immutable, fully decrypted view of the desired
// state. The renderer works only from a Snapshot, never from the store, so
// rendering is a pure function of this value.
package desired

import (
	"context"
	"fmt"

	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Snapshot is the desired state at one generation.
type Snapshot struct {
	Generation     int64
	Network        model.NetworkConfig
	DNS            model.DNSConfig
	Nodes          []model.Node // all nodes, including disabled; Credential populated
	Groups         []model.OutboundGroup
	RuleSets       []model.RuleSet
	Rules          []model.Rule // ordered by set position, rule position
	DevicePolicies []model.DevicePolicy
	Bypass         []model.BypassDestination
	// Resolved is the DNS cache the Mode C route engine reads; it is derived
	// state maintained by the resolver, not something the user edits.
	Resolved []store.ResolvedEntry
	// ProtectedHosts are managed remote hosts whose addresses must never be
	// published as routes.
	ProtectedHosts []string
}

// Load reads a consistent snapshot. SQLite WAL gives each statement a stable
// view; the generation is read first and compared after so a write that
// lands mid-load is detected and the load retried once.
func Load(ctx context.Context, st *store.Store, kr *secret.Keyring) (*Snapshot, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before, err := st.Meta(ctx)
		if err != nil {
			return nil, err
		}
		snap, err := load(ctx, st, kr)
		if err != nil {
			return nil, err
		}
		after, err := st.Meta(ctx)
		if err != nil {
			return nil, err
		}
		if before.Generation == after.Generation {
			snap.Generation = after.Generation
			return snap, nil
		}
	}
	return nil, fmt.Errorf("desired state kept changing during load")
}

func load(ctx context.Context, st *store.Store, kr *secret.Keyring) (*Snapshot, error) {
	var s Snapshot
	var err error
	if s.Network, err = st.NetworkConfig(ctx); err != nil {
		return nil, err
	}
	if s.DNS, err = st.DNSConfig(ctx); err != nil {
		return nil, err
	}
	rows, err := st.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	s.Nodes = make([]model.Node, 0, len(rows))
	for _, r := range rows {
		n := r.Node
		if kr != nil {
			if n.Credential, err = kr.Decrypt(r.CredentialEnc); err != nil {
				return nil, fmt.Errorf("node %s (%s): %w", n.ID, n.Name, err)
			}
		}
		s.Nodes = append(s.Nodes, n)
	}
	if s.Groups, err = st.ListGroups(ctx); err != nil {
		return nil, err
	}
	if s.RuleSets, err = st.ListRuleSets(ctx); err != nil {
		return nil, err
	}
	if s.Rules, err = st.ListRules(ctx, ""); err != nil {
		return nil, err
	}
	if s.DevicePolicies, err = st.ListDevicePolicies(ctx); err != nil {
		return nil, err
	}
	if s.Bypass, err = st.ListBypassDestinations(ctx); err != nil {
		return nil, err
	}
	if s.Resolved, err = st.ListResolveCache(ctx); err != nil {
		return nil, err
	}
	if s.ProtectedHosts, err = st.ListRemoteHostAddrs(ctx); err != nil {
		return nil, err
	}
	return &s, nil
}

// Node returns the node with id, or nil.
func (s *Snapshot) Node(id string) *model.Node {
	for i := range s.Nodes {
		if s.Nodes[i].ID == id {
			return &s.Nodes[i]
		}
	}
	return nil
}

// Group returns the group with id, or nil.
func (s *Snapshot) Group(id string) *model.OutboundGroup {
	for i := range s.Groups {
		if s.Groups[i].ID == id {
			return &s.Groups[i]
		}
	}
	return nil
}

// EnabledNodes returns enabled nodes in stable order.
func (s *Snapshot) EnabledNodes() []model.Node {
	out := make([]model.Node, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		if n.Enabled {
			out = append(out, n)
		}
	}
	return out
}

// DefaultNode picks the default outbound node: the flagged default if it is
// enabled, otherwise the only enabled node if there is exactly one, else nil.
func (s *Snapshot) DefaultNode() *model.Node {
	enabled := s.EnabledNodes()
	for i := range enabled {
		if enabled[i].IsDefault {
			return &enabled[i]
		}
	}
	if len(enabled) == 1 {
		return &enabled[0]
	}
	return nil
}

// ActiveRules returns enabled rules belonging to enabled sets, in order.
func (s *Snapshot) ActiveRules() []model.Rule {
	enabledSets := map[string]bool{}
	for _, rs := range s.RuleSets {
		if rs.Enabled {
			enabledSets[rs.ID] = true
		}
	}
	out := make([]model.Rule, 0, len(s.Rules))
	for _, r := range s.Rules {
		if r.Enabled && enabledSets[r.SetID] {
			out = append(out, r)
		}
	}
	return out
}
