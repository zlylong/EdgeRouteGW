import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, request, setUnauthenticatedHandler } from './client'

const g = globalThis as unknown as { fetch: typeof fetch; document: { cookie: string } }

function mockFetch(status: number, body: unknown, ct = 'application/json') {
  g.fetch = vi.fn(async () => new Response(typeof body === 'string' ? body : JSON.stringify(body), {
    status, headers: { 'content-type': ct },
  })) as unknown as typeof fetch
}

afterEach(() => { vi.restoreAllMocks(); setUnauthenticatedHandler(() => {}) })

describe('request', () => {
  it('parses JSON and sends CSRF on mutations', async () => {
    g.document = { cookie: 'ergw_csrf=tok123; other=1' }
    mockFetch(200, { item: { id: 'x' } })
    const out = await request<{ item: { id: string } }>('/api/v2/nodes', { method: 'POST', body: { a: 1 } })
    expect(out.item.id).toBe('x')
    const init = (g.fetch as unknown as { mock: { calls: [string, RequestInit][] } }).mock.calls[0][1]
    expect((init.headers as Record<string, string>)['X-CSRF-Token']).toBe('tok123')
    expect(init.credentials).toBe('same-origin')
  })
  it('maps the error envelope and fires the 401 hook', async () => {
    g.document = { cookie: '' }
    const hook = vi.fn()
    setUnauthenticatedHandler(hook)
    mockFetch(401, { error: { code: 'UNAUTHENTICATED', message: 'login required' } })
    await expect(request('/api/v2/state')).rejects.toMatchObject({ status: 401, code: 'UNAUTHENTICATED' })
    expect(hook).toHaveBeenCalledOnce()
  })
  it('exposes validation field', async () => {
    g.document = { cookie: '' }
    mockFetch(422, { error: { code: 'VALIDATION_FAILED', message: 'port: must be 1-65535', details: { field: 'port' } } })
    try { await request('/api/v2/nodes', { method: 'POST', body: {} }) } catch (e) {
      expect(e).toBeInstanceOf(ApiError)
      expect((e as ApiError).field).toBe('port')
    }
  })
})
