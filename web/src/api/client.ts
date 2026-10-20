// Thin fetch wrapper: same-origin cookies, CSRF header on mutations, uniform
// error type, and a hook the auth store uses to react to 401s.

export class ApiError extends Error {
  status: number
  code: string
  details?: Record<string, unknown>
  constructor(status: number, code: string, message: string, details?: Record<string, unknown>) {
    super(message)
    this.status = status
    this.code = code
    this.details = details
  }
  get field(): string | undefined {
    const f = this.details?.field
    return typeof f === 'string' ? f : undefined
  }
}

let onUnauthenticated: (() => void) | null = null
/** Registers the callback invoked when any request returns 401. */
export function setUnauthenticatedHandler(fn: () => void) { onUnauthenticated = fn }

export function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)ergw_csrf=([^;]+)/)
  return m ? decodeURIComponent(m[1]) : ''
}

export interface RequestOpts { method?: string; body?: unknown; signal?: AbortSignal }

export async function request<T>(path: string, opts: RequestOpts = {}): Promise<T> {
  const method = (opts.method ?? 'GET').toUpperCase()
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && method !== 'HEAD') {
    const t = csrfToken()
    if (t) headers['X-CSRF-Token'] = t
  }
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: opts.signal,
    })
  } catch (e) {
    throw new ApiError(0, 'NETWORK', '无法连接到网关', { cause: String(e) })
  }
  if (res.status === 204) return undefined as T
  const text = await res.text()
  const ct = res.headers.get('content-type') ?? ''
  let data: unknown = text
  if (ct.includes('application/json') && text) {
    try { data = JSON.parse(text) } catch { /* keep text */ }
  }
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string; details?: Record<string, unknown> } })?.error
    if (res.status === 401 && onUnauthenticated && !path.endsWith('/auth/login')) onUnauthenticated()
    throw new ApiError(res.status, err?.code ?? `HTTP_${res.status}`, err?.message ?? res.statusText, err?.details)
  }
  return data as T
}

export const api = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>(path, { signal }),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body }),
  put: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PUT', body }),
  del: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
  text: async (path: string): Promise<string> => {
    const res = await fetch(path, { credentials: 'same-origin' })
    if (res.status === 401 && onUnauthenticated) onUnauthenticated()
    if (!res.ok) throw new ApiError(res.status, `HTTP_${res.status}`, res.statusText)
    return res.text()
  },
}
