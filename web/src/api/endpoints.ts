import { api } from './client'
import type * as T from './types'

const B = '/api/v2'

export const auth = {
  me: () => api.get<T.Me>(`${B}/auth/me`),
  login: (password: string) => api.post<T.Me>(`${B}/auth/login`, { password }),
  logout: () => api.post<T.Me>(`${B}/auth/logout`),
  changePassword: (old_password: string, new_password: string) => api.post<{ changed: boolean }>(`${B}/auth/password`, { old_password, new_password }),
  sessions: () => api.get<T.ListResp<T.SessionView>>(`${B}/auth/sessions`),
  deleteSession: (id: string) => api.del<{ deleted: number }>(`${B}/auth/sessions/${id}`),
  tokens: () => api.get<T.ListResp<T.APIToken>>(`${B}/auth/tokens`),
  createToken: (name: string, scope: 'read' | 'write', expires_in: string) => api.post<{ item: T.APIToken; token: string }>(`${B}/auth/tokens`, { name, scope, expires_in }),
  deleteToken: (id: string) => api.del<{ deleted: number }>(`${B}/auth/tokens/${id}`),
}

export const system = {
  state: () => api.get<T.StateResponse>(`${B}/state`),
  plan: () => api.get<T.PlanResponse>(`${B}/plan`),
  reconcile: () => api.post<{ state: T.StateResponse }>(`${B}/reconcile`),
  artifact: (name: 'xray' | 'mosdns' | 'mosdns-domains' | 'nftables') => api.text(`${B}/artifacts/${name}`),
  events: (q: { module?: string; level?: string; before_id?: number; limit?: number } = {}) => {
    const p = new URLSearchParams()
    if (q.module) p.set('module', q.module)
    if (q.level) p.set('level', q.level)
    if (q.before_id) p.set('before_id', String(q.before_id))
    if (q.limit) p.set('limit', String(q.limit))
    return api.get<T.ListResp<T.Event>>(`${B}/events?${p}`)
  },
  changesets: (limit = 100) => api.get<T.ListResp<T.Changeset>>(`${B}/changesets?limit=${limit}`),
  applyLog: (generation?: number, limit = 200) => api.get<T.ListResp<T.ApplyLogEntry>>(`${B}/apply-log?limit=${limit}${generation ? `&generation=${generation}` : ''}`),
  connections: (limit = 200, q = '') => api.get<T.ListResp<T.Connection>>(`${B}/connections?limit=${limit}&q=${encodeURIComponent(q)}`),
  trace: (target: string) => api.get<T.TraceResult>(`${B}/trace?target=${encodeURIComponent(target)}`),
}

export const nodes = {
  list: () => api.get<T.ListResp<T.Node>>(`${B}/nodes`),
  create: (input: T.NodeInput) => api.post<T.ItemResp<T.Node>>(`${B}/nodes`, input),
  update: (id: string, input: T.NodeInput) => api.put<T.ItemResp<T.Node>>(`${B}/nodes/${id}`, input),
  remove: (id: string) => api.del<T.GenResp>(`${B}/nodes/${id}`),
  setDefault: (id: string) => api.post<T.GenResp>(`${B}/nodes/${id}/default`),
  setEnabled: (id: string, enabled: boolean) => api.post<T.GenResp>(`${B}/nodes/${id}/enabled`, { enabled }),
  import: (links: string[], dry_run: boolean) => api.post<T.ImportResponse>(`${B}/node-imports`, { links, dry_run }),
}

export const groups = {
  list: () => api.get<T.ListResp<T.OutboundGroup>>(`${B}/groups`),
  create: (input: T.GroupInput) => api.post<T.ItemResp<T.OutboundGroup>>(`${B}/groups`, input),
  update: (id: string, input: T.GroupInput) => api.put<T.ItemResp<T.OutboundGroup>>(`${B}/groups/${id}`, input),
  remove: (id: string) => api.del<T.GenResp>(`${B}/groups/${id}`),
}

export const routing = {
  sets: () => api.get<T.ListResp<T.RuleSet>>(`${B}/rule-sets`),
  createSet: (name: string) => api.post<T.ItemResp<T.RuleSet>>(`${B}/rule-sets`, { name }),
  updateSet: (id: string, input: { name?: string; enabled?: boolean }) => api.put<T.ItemResp<T.RuleSet>>(`${B}/rule-sets/${id}`, input),
  removeSet: (id: string) => api.del<T.GenResp>(`${B}/rule-sets/${id}`),
  reorderSets: (ids: string[]) => api.post<T.GenResp>(`${B}/rule-set-order`, { ids }),
  rules: (setId?: string) => api.get<T.ListResp<T.Rule>>(`${B}/rules${setId ? `?set_id=${setId}` : ''}`),
  createRules: (input: T.RuleInput) => api.post<{ items: T.Rule[]; generation: number }>(`${B}/rules`, input),
  updateRule: (id: string, input: T.RuleInput) => api.put<T.ItemResp<T.Rule>>(`${B}/rules/${id}`, input),
  removeRule: (id: string) => api.del<T.GenResp>(`${B}/rules/${id}`),
  reorderRules: (setId: string, ids: string[]) => api.post<T.GenResp>(`${B}/rule-sets/${setId}/reorder`, { ids }),
  moveRule: (id: string, set_id: string) => api.post<T.GenResp>(`${B}/rules/${id}/move`, { set_id }),
  devices: () => api.get<T.ListResp<T.DevicePolicy>>(`${B}/device-policies`),
  createDevice: (input: T.DevicePolicyInput) => api.post<T.ItemResp<T.DevicePolicy>>(`${B}/device-policies`, input),
  updateDevice: (id: string, input: T.DevicePolicyInput) => api.put<T.ItemResp<T.DevicePolicy>>(`${B}/device-policies/${id}`, input),
  removeDevice: (id: string) => api.del<T.GenResp>(`${B}/device-policies/${id}`),
  bypass: () => api.get<T.ListResp<T.BypassDestination>>(`${B}/bypass`),
  createBypass: (cidr: string, remark: string) => api.post<T.ItemResp<T.BypassDestination>>(`${B}/bypass`, { cidr, remark }),
  removeBypass: (id: string) => api.del<T.GenResp>(`${B}/bypass/${id}`),
}

export const network = {
  ospf: () => api.get<T.OSPFResponse>(`${B}/ospf`),
  routes: (q: { state?: string; q?: string; limit?: number } = {}) => {
    const p = new URLSearchParams()
    if (q.state) p.set('state', q.state)
    if (q.q) p.set('q', q.q)
    if (q.limit) p.set('limit', String(q.limit))
    return api.get<T.ListResp<T.PublishedRoute>>(`${B}/routes?${p}`)
  },
  resetRoutes: () => api.post<{ cleared: boolean }>(`${B}/routes/reset`),
  interfaces: () => api.get<T.ListResp<T.InterfaceView>>(`${B}/interfaces`),
  preflight: (mode: T.Mode) => api.post<T.PreflightResult>(`${B}/network/preflight`, { mode }),
  clearDnsCache: () => api.post<{ cleared: boolean }>(`${B}/dns/cache-clear`),
}

export const hosts = {
  list: () => api.get<T.ListResp<T.RemoteHost>>(`${B}/hosts`),
  get: (id: string) => api.get<T.ItemResp<T.RemoteHost>>(`${B}/hosts/${id}`),
  create: (input: T.HostInput) => api.post<T.ItemResp<T.RemoteHost>>(`${B}/hosts`, input),
  update: (id: string, input: T.HostInput) => api.put<T.ItemResp<T.RemoteHost>>(`${B}/hosts/${id}`, input),
  remove: (id: string, cleanup: boolean) => api.del<{ deleted: number; remote_cleaned: boolean }>(`${B}/hosts/${id}${cleanup ? '?cleanup=1' : ''}`),
  check: (id: string) => api.post<{ status: T.HostStatus; reason?: string }>(`${B}/hosts/${id}/check`),
  deploy: (id: string, req: T.DeployRequest) => api.post<{ deploy_id: string }>(`${B}/hosts/${id}/deploy`, req),
  rePin: (id: string, fingerprint: string) => api.post<{ host_key: string }>(`${B}/hosts/${id}/hostkey`, { fingerprint }),
  deploys: (hostId?: string) => api.get<T.ListResp<T.RemoteDeploy>>(`${B}/deploys${hostId ? `?host_id=${hostId}` : ''}`),
  deploy_get: (id: string) => api.get<T.ItemResp<T.RemoteDeploy>>(`${B}/deploys/${id}`),
  deployLogs: (id: string, afterId = 0) => api.get<T.ListResp<T.DeployLogLine>>(`${B}/deploys/${id}/logs?after_id=${afterId}`),
  rollback: (deployId: string) => api.post<{ deploy_id: string }>(`${B}/deploys/${deployId}/rollback`),
}

export const components = {
  list: (refresh = false) => api.get<T.ListResp<T.ComponentInfo>>(`${B}/components${refresh ? '?refresh=1' : ''}`),
  versions: (name: string) => api.get<T.ListResp<T.Release>>(`${B}/components/${name}/versions`),
  update: (name: string, version?: string) => api.post<T.ItemResp<T.UpdateJob>>(`${B}/components/${name}/update`, version ? { version } : undefined),
  rollback: (name: string) => api.post<T.ItemResp<T.UpdateJob>>(`${B}/components/${name}/rollback`),
  schedule: () => api.get<{ schedule: string }>(`${B}/components/geodata-schedule`),
  setSchedule: (schedule: string) => api.put<{ schedule: string }>(`${B}/components/geodata-schedule`, { schedule }),
}

export const config = {
  dns: () => api.get<T.ItemResp<T.DNSConfig>>(`${B}/dns`),
  putDns: (c: T.DNSConfig) => api.put<T.ItemResp<T.DNSConfig>>(`${B}/dns`, stripTs(c)),
  network: () => api.get<T.ItemResp<T.NetworkConfig>>(`${B}/network`),
  putNetwork: (c: T.NetworkConfig) => api.put<T.ItemResp<T.NetworkConfig>>(`${B}/network`, stripTs(c)),
}

function stripTs<T extends { updated_at?: string }>(c: T): Omit<T, 'updated_at'> {
  const { updated_at: _omit, ...rest } = c
  return rest
}
