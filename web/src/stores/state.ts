// Live gateway state: loaded once over REST, then kept current by the SSE
// stream. Pages read `state`; the top bar reads `connected` and the apply
// status; the observe page subscribes to `lastEvent` for live connections.
import { defineStore } from 'pinia'
import { computed, ref, shallowRef } from 'vue'
import { system } from '@/api/endpoints'
import type { Connection, SseEvent, StateResponse } from '@/api/types'
import { useToast } from './toast'

export type StreamStatus = 'idle' | 'connecting' | 'open' | 'retrying'

export const useGatewayState = defineStore('gateway-state', () => {
  const state = shallowRef<StateResponse | null>(null)
  const stream = ref<StreamStatus>('idle')
  const lastEvent = shallowRef<SseEvent | null>(null)
  const liveConnections = ref<Connection[]>([])
  let es: EventSource | null = null
  let retryTimer: number | null = null
  let retryDelay = 1000

  const components = computed(() => state.value?.components ?? [])
  const pending = computed(() => !!state.value && (state.value.pending || state.value.applying))
  const degraded = computed(() => components.value.some(c => c.status === 'degraded' || c.status === 'down'))

  async function load() {
    state.value = await system.state()
  }

  function connect() {
    if (es) return
    stream.value = 'connecting'
    es = new EventSource('/api/v2/events/stream')
    const toast = useToast()
    es.addEventListener('state', ev => { state.value = JSON.parse((ev as MessageEvent).data); stream.value = 'open'; retryDelay = 1000 })
    const relay = (type: string) => es!.addEventListener(type, ev => {
      lastEvent.value = { type, at: new Date().toISOString(), data: JSON.parse((ev as MessageEvent).data) }
    })
    for (const t of ['state.changed', 'apply.started', 'apply.finished', 'component.health', 'provision.log', 'provision.state', 'provision.host', 'update.progress', 'update.done', 'traffic.sample', 'nodes.latency']) relay(t)
    es.addEventListener('apply.failed', ev => {
      const d = JSON.parse((ev as MessageEvent).data) as { generation: number; error: string; retry_in_s: number }
      lastEvent.value = { type: 'apply.failed', at: new Date().toISOString(), data: d }
      toast.error(`下发失败(gen ${d.generation})`, `${d.error}。${d.retry_in_s} 秒后重试。`)
    })
    es.addEventListener('connection', ev => {
      const c = JSON.parse((ev as MessageEvent).data) as Connection
      liveConnections.value.unshift(c)
      if (liveConnections.value.length > 500) liveConnections.value.length = 500
    })
    es.onopen = () => { stream.value = 'open' }
    es.onerror = () => {
      // EventSource retries on its own for transient errors, but a 401 closes
      // it for good; back off and reconnect ourselves either way.
      es?.close(); es = null
      stream.value = 'retrying'
      if (retryTimer) window.clearTimeout(retryTimer)
      retryTimer = window.setTimeout(() => { retryTimer = null; connect() }, retryDelay)
      retryDelay = Math.min(retryDelay * 2, 15000)
    }
  }
  function disconnect() {
    es?.close(); es = null
    if (retryTimer) { window.clearTimeout(retryTimer); retryTimer = null }
    stream.value = 'idle'
  }

  return { state, stream, lastEvent, liveConnections, components, pending, degraded, load, connect, disconnect }
})
