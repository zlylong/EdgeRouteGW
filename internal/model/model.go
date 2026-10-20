// Package model holds the domain types shared by store, render and api.
// It has no dependencies on persistence or HTTP.
package model

import "time"

// Mode is the LAN takeover mode. See docs/DESIGN-2.0.md section 1.
type Mode string

const (
	ModeA Mode = "A" // TPROXY global gateway
	ModeB Mode = "B" // Fake-IP + OSPF announcing the fake pool
	ModeC Mode = "C" // real-IP OSPF with domain tracking
)

// Valid reports whether m is one of the three supported modes.
func (m Mode) Valid() bool { return m == ModeA || m == ModeB || m == ModeC }

// Component names the external systems the reconciler drives. The order is
// the order the UI shows them in.
type Component string

const (
	ComponentXray     Component = "xray"
	ComponentMosdns   Component = "mosdns"
	ComponentNftables Component = "nftables"
	ComponentFRR      Component = "frr"
)

// Components lists every component in display order.
var Components = []Component{ComponentXray, ComponentMosdns, ComponentNftables, ComponentFRR}

// ComponentStatus is the reconciler's view of one component.
type ComponentStatus string

const (
	StatusUnknown  ComponentStatus = "unknown"  // no driver has reported yet
	StatusActive   ComponentStatus = "active"   // running and last apply succeeded
	StatusDegraded ComponentStatus = "degraded" // running but last apply failed
	StatusDown     ComponentStatus = "down"     // unit not running
	StatusDisabled ComponentStatus = "disabled" // not used in the current mode
)

// ComponentHealth is what /api/v2/state reports per component.
type ComponentHealth struct {
	Name      Component       `json:"name"`
	Status    ComponentStatus `json:"status"`
	Version   string          `json:"version,omitempty"`
	LastError string          `json:"last_error,omitempty"`
	LastApply *time.Time      `json:"last_apply,omitempty"`
}

// Meta is the single-row generation counter driving the reconciler.
type Meta struct {
	Generation            int64     `json:"generation"`
	LastAppliedGeneration int64     `json:"last_applied_generation"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// Protocol is a node's outbound protocol.
type Protocol string

const (
	ProtocolVMess       Protocol = "vmess"
	ProtocolVLESS       Protocol = "vless"
	ProtocolTrojan      Protocol = "trojan"
	ProtocolShadowsocks Protocol = "shadowsocks"
	ProtocolSocks       Protocol = "socks"
	ProtocolHTTP        Protocol = "http"
	ProtocolWireGuard   Protocol = "wireguard"
	ProtocolCustom      Protocol = "custom"
)

// Node is an outbound proxy node.
type Node struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Protocol  Protocol  `json:"protocol"`
	Address   string    `json:"address"`
	Port      int       `json:"port"`
	Params    string    `json:"params"` // protocol-specific JSON, never holds the credential
	Enabled   bool      `json:"enabled"`
	IsDefault bool      `json:"is_default"`
	LatencyMs *int      `json:"latency_ms,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GroupKind is how an outbound group picks a member.
type GroupKind string

const (
	GroupFailover    GroupKind = "failover"
	GroupLoadBalance GroupKind = "loadbalance"
)

// OutboundGroup replaces the 1.x "bal-ha-<a>-<b>" policy string.
type OutboundGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      GroupKind `json:"kind"`
	MemberIDs []string  `json:"member_ids"` // ordered; first is primary for failover
	Strategy  string    `json:"strategy"`   // kind-specific JSON
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RuleSet is an ordered, toggleable group of rules.
type RuleSet struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Position  int       `json:"position"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MatchKind is what a rule matches on.
type MatchKind string

const (
	MatchDomain  MatchKind = "domain"
	MatchIP      MatchKind = "ip"
	MatchGeosite MatchKind = "geosite"
	MatchGeoIP   MatchKind = "geoip"
)

// ActionKind is what happens to traffic a rule matches.
type ActionKind string

const (
	ActionDirect ActionKind = "direct"
	ActionProxy  ActionKind = "proxy" // default outbound
	ActionBlock  ActionKind = "block"
	ActionNode   ActionKind = "node"  // ActionRef = Node.ID
	ActionGroup  ActionKind = "group" // ActionRef = OutboundGroup.ID
)

// Rule is one routing rule inside a RuleSet.
type Rule struct {
	ID         string     `json:"id"`
	SetID      string     `json:"set_id"`
	Position   int        `json:"position"`
	MatchKind  MatchKind  `json:"match_kind"`
	MatchValue string     `json:"match_value"`
	ActionKind ActionKind `json:"action_kind"`
	ActionRef  string     `json:"action_ref,omitempty"`
	Enabled    bool       `json:"enabled"`
	Remark     string     `json:"remark,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// DevicePolicyKind identifies a LAN device.
type DevicePolicyKind string

const (
	DeviceMAC  DevicePolicyKind = "mac"
	DeviceIP   DevicePolicyKind = "ip"
	DeviceCIDR DevicePolicyKind = "cidr"
)

// DeviceAction is the per-device override applied in nftables.
type DeviceAction string

const (
	DeviceProxy  DeviceAction = "proxy"
	DeviceDirect DeviceAction = "direct"
	DeviceReject DeviceAction = "reject"
)

// DevicePolicy is the 2.0 name for a 1.x LAN ACL entry.
type DevicePolicy struct {
	ID        string           `json:"id"`
	Kind      DevicePolicyKind `json:"kind"`
	Value     string           `json:"value"`
	Action    DeviceAction     `json:"action"`
	Remark    string           `json:"remark,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// BypassDestination is a destination CIDR that skips Xray in Mode A
// (1.x "protected IP").
type BypassDestination struct {
	ID        string    `json:"id"`
	CIDR      string    `json:"cidr"`
	Remark    string    `json:"remark,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// DNSConfig is the single-row Mosdns configuration.
type DNSConfig struct {
	LocalUpstreams  []string  `json:"local_upstreams"`
	RemoteUpstreams []string  `json:"remote_upstreams"`
	CacheSize       int       `json:"cache_size"`
	LazyCache       bool      `json:"lazy_cache"`
	LazyCacheTTL    int       `json:"lazy_cache_ttl"`
	LogLevel        string    `json:"log_level"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// NetworkConfig is the single-row network/mode configuration.
type NetworkConfig struct {
	Mode                Mode      `json:"mode"`
	ManagementIface     string    `json:"management_iface"`
	ServiceIface        string    `json:"service_iface"`
	FakeIPPool          string    `json:"fakeip_pool"`
	TProxyPort          int       `json:"tproxy_port"`
	LANDefaultPolicy    string    `json:"lan_default_policy"`
	OSPFPushBatch       int       `json:"ospf_push_batch"`
	OSPFPushIntervalSec int       `json:"ospf_push_interval_s"`
	OSPFResolveWorkers  int       `json:"ospf_resolve_workers"`
	OSPFPublishAllow    []string  `json:"ospf_publish_allowlist"`
	NodeFailoverMode    string    `json:"node_failover_mode"`
	UpdatedAt           time.Time `json:"updated_at"`
}
