// Types mirror the Go JSON shapes in internal/api/v2 and internal/model.

export type Mode = 'A' | 'B' | 'C'
export type Component = 'xray' | 'mosdns' | 'nftables' | 'frr'
export type ComponentStatus = 'unknown' | 'active' | 'degraded' | 'down' | 'disabled'

export interface ComponentHealth {
  name: Component
  status: ComponentStatus
  version?: string
  last_error?: string
  last_apply?: string
}

export interface VersionInfo {
  version: string
  commit: string
  build_time: string
  go: string
  os: string
  arch: string
}

export interface StateResponse {
  version: VersionInfo
  mode: Mode
  generation: number
  last_applied_generation: number
  pending: boolean
  applying: boolean
  last_error?: string
  retry_in_s?: number
  components: ComponentHealth[]
  warnings: string[]
  started_at: string
  uptime_s: number
  schema_version: string
}

export type ActionKind = 'noop' | 'write' | 'hot' | 'restart' | 'start' | 'exec' | 'skipped'
export interface PlanAction { kind: ActionKind; detail: string }
export interface Plan { component: Component; actions: PlanAction[]; diff?: string }
export interface PlanResponse {
  generation: number
  plans: Plan[]
  warnings: string[]
  artifact_sizes: Record<string, number>
}

export type Protocol = 'vmess' | 'vless' | 'trojan' | 'shadowsocks' | 'socks' | 'http' | 'wireguard' | 'custom'

export interface Node {
  id: string
  name: string
  protocol: Protocol
  address: string
  port: number
  params: string
  enabled: boolean
  is_default: boolean
  latency_ms?: number
  probe?: { node_id: string; ms: number; error?: string; via: 'xray' | 'tcp'; checked_at: string }
  has_credential: boolean
  created_at: string
  updated_at: string
}

export interface NodeInput {
  name: string
  protocol: Protocol
  address: string
  port: number
  params: string
  enabled?: boolean
  credential?: string
}

export interface ImportResult { link: string; node?: Node; error?: string }
export interface ImportResponse { results: ImportResult[]; imported: number; generation?: number; dry_run?: boolean }

export type GroupKind = 'failover' | 'loadbalance'
export interface OutboundGroup {
  id: string
  name: string
  kind: GroupKind
  member_ids: string[]
  strategy: string
  created_at: string
  updated_at: string
}
export interface GroupInput { name: string; kind: GroupKind; member_ids: string[]; strategy?: string }

export interface RuleSet {
  id: string; name: string; position: number; enabled: boolean; builtin?: boolean; created_at: string; updated_at: string }

export type MatchKind = 'domain' | 'ip' | 'geosite' | 'geoip'
export type RuleAction = 'direct' | 'proxy' | 'block' | 'node' | 'group'
export interface Rule {
  id: string
  set_id: string
  position: number
  match_kind: MatchKind
  match_value: string
  action_kind: RuleAction
  action_ref?: string
  enabled: boolean
  remark?: string
  created_at: string
  updated_at: string
}
export interface RuleInput {
  set_id: string
  match_kind: MatchKind
  match_value: string
  action_kind: RuleAction
  action_ref?: string
  enabled?: boolean
  remark?: string
}

export type DeviceKind = 'mac' | 'ip' | 'cidr'
export type DeviceAction = 'proxy' | 'direct' | 'reject'
export interface DevicePolicy { id: string; kind: DeviceKind; value: string; action: DeviceAction; remark?: string; created_at: string; updated_at: string }
export interface DevicePolicyInput { kind: DeviceKind; value: string; action: DeviceAction; remark?: string }

export interface BypassDestination { id: string; cidr: string; remark?: string; created_at: string }

export interface DNSConfig {
  local_upstreams: string[]
  remote_upstreams: string[]
  hijack?: boolean
  block_dot?: boolean
  cache_size: number
  lazy_cache: boolean
  lazy_cache_ttl: number
  log_level: string
  updated_at?: string
}

export interface NetworkConfig {
  mode: Mode
  management_iface: string
  service_iface: string
  fakeip_pool: string
  tproxy_port: number
  lan_default_policy: 'proxy' | 'direct'
  ospf_push_batch: number
  ospf_push_interval_s: number
  ospf_resolve_workers: number
  ospf_publish_allowlist: string[]
  node_failover_mode: 'normal' | 'strict'
  updated_at?: string
}

export interface Changeset { id: string; generation: number; actor?: string; summary: string; diff?: string; reverted_by?: string; created_at: string }
export interface ApplyLogEntry { id: number; generation: number; component: Component; action: string; ok: boolean; duration_ms: number; error?: string; ts: string }
export type EventLevel = 'debug' | 'info' | 'warn' | 'error'
export interface Event { id: number; ts: string; level: EventLevel; module: string; type: string; message: string; trace_id?: string; actor?: string; details?: string }

export interface Connection { at: string; source: string; network: string; target: string; inbound: string; outbound: string; accepted: boolean; geo?: string[] }
export interface GeoLookup { target: string; kind: 'domain' | 'ip'; geosite: string[]; geoip: string[] }
export interface GeoEntries { kind: 'geosite' | 'geoip'; tag: string; total: number; items: string[] }

export interface TraceResult {
  target: string
  kind: 'domain' | 'ip'
  matched_rule?: Rule
  rule_set?: string
  action: string
  outbound: string
  via?: string
  reason: string
  considered: number
  notes?: string[]
}

export interface Me {
  authenticated: boolean
  kind?: 'session' | 'token'
  name?: string
  scope?: string
  must_change_password: boolean
  csrf_token?: string
}

export interface SessionView { id: string; current: boolean; created_at: string; last_seen_at: string; expires_at: string; remote_ip: string; user_agent: string }
export interface APIToken { id: string; name: string; scope: 'read' | 'write'; created_at: string; last_used_at?: string; expires_at?: string }

export interface ListResp<T> { items: T[] }
export interface ItemResp<T> { item: T; generation?: number }
export interface GenResp { generation: number; deleted?: number; retargeted_rules?: number }

export interface SseEvent<T = unknown> { type: string; at: string; data: T }

export interface OSPFNeighbor { router_id: string; address: string; interface: string; state: string; priority: number; dead_time: string }
export interface OSPFResponse {
  mode: Mode
  enabled: boolean
  service_iface: string
  lan_prefix?: string
  neighbors: OSPFNeighbor[]
  neighbors_error?: string
  routes: Record<'published' | 'pending' | 'failed' | 'protected', number>
  resolver: { cached: number; expired: number; failing: number }
}
export interface PublishedRoute { cidr: string; source: string; rule_id?: string; state: string; detail?: string; first_seen: string; last_seen: string }
export interface InterfaceView { name: string; up: boolean; loopback: boolean; addresses: string[]; mac?: string }
export interface PreflightResult { ok: boolean; mode: string; routes: number; rejected: number; protected_conflicts: string[] | null; problems: string[] | null; warnings: string[] | null }

export type HostStatus = 'unknown' | 'online' | 'offline' | 'deploying' | 'failed'
export interface RemoteDeploy { id: string; host_id: string; kind: 'wg' | 'vless'; state: 'queued' | 'running' | 'succeeded' | 'failed' | 'rolled_back'; node_id?: string; started_at?: string; finished_at?: string; error?: string; created_at: string; params?: Record<string, unknown> }
export interface HostTuneReport { at: string; items: TuningItem[]; error?: string }
export interface RemoteHost { id: string; name: string; ssh_host: string; ssh_port: number; ssh_user: string; ssh_auth_kind: 'password' | 'key'; host_key: string; region: string; status: HostStatus; remark: string; created_at: string; updated_at: string; has_credential: boolean; last_deploy?: RemoteDeploy; tune?: HostTuneReport }
export interface HostInput { name: string; ssh_host: string; ssh_port: number; ssh_user: string; ssh_auth_kind: 'password' | 'key'; credential?: string; region?: string; remark?: string }
export interface DeployRequest { kind: 'wg' | 'vless'; port?: number; server_name?: string; dest?: string; node_name?: string }
export interface DeployLogLine { id: number; at: string; stream: 'stdout' | 'stderr' | 'system'; line: string }

export interface UpdateJob { id: string; component: string; version: string; action: 'update' | 'rollback' | 'install'; state: 'running' | 'succeeded' | 'failed'; step: string; percent: number; error?: string; started_at: string; ended_at?: string }
export interface ComponentInfo { name: 'xray' | 'mosdns' | 'geodata' | 'edgeroutegw'; installed: string; has_previous: boolean; bundled?: { version: string }; latest?: string; job?: UpdateJob; last_error?: string; checked_at?: string }
export interface Release { tag: string; prerelease: boolean; published_at: string }

export interface TrafficPoint { ts: number; up: number; down: number }
export interface NodeTraffic { node_id: string; up: number; down: number }
export interface TrafficRate { up_bps: number; down_bps: number }
export interface TrafficRates { at: string; up_bps: number; down_bps: number; nodes: Record<string, TrafficRate>; available: boolean }
export interface TrafficResponse { range: string; bucket_s: number; series: TrafficPoint[]; total_up: number; total_down: number; nodes: NodeTraffic[]; current: TrafficRates }
export interface LatencyResult { node_id: string; ms: number; error?: string }
export interface DownloadSettings { proxy: string; github_mirror: string }
export interface GeoTags { kind: 'geosite' | 'geoip'; installed: boolean; version?: string; items: string[] }
export interface TuningItem { key: string; title: string; status: 'ok' | 'applied' | 'fallback' | 'unavailable' | 'info'; detail: string; hint?: string }
export interface TuningReport { at: string; applied: boolean; host: { cpus: number; kernel: string; virt: string; iface: string }; items: TuningItem[] }
