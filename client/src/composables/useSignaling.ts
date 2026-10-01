import { ref, shallowRef } from 'vue'
import type { Envelope } from '../types/protocol'

export type SignalingStatus = 'idle' | 'connecting' | 'open' | 'closed'

export interface SignalingHandlers {
  onMessage: (env: Envelope) => void
  onOpen?: () => void
  onClose?: (reason: string) => void
}

const MAX_RECONNECT_DELAY_MS = 8000

/**
 * 信令通道：只负责 WebSocket 的收发、重连与消息分发。
 * 它不理解任何业务语义，房间状态全部在 store 里。
 *
 * 断线重连后必须重新 join：服务端不保留已断开连接的身份（SPEC §5.1）。
 */
export function useSignaling(handlers: SignalingHandlers) {
  const status = ref<SignalingStatus>('idle')
  const socket = shallowRef<WebSocket | null>(null)

  const pending: string[] = []
  let currentUrl = ''
  let reconnectTimer: number | undefined
  let reconnectAttempt = 0
  let closedByUs = false

  function connect(url: string) {
    currentUrl = url
    closedByUs = false
    open()
  }

  function open() {
    if (!currentUrl) return

    status.value = 'connecting'
    const ws = new WebSocket(currentUrl)
    socket.value = ws

    ws.onopen = () => {
      status.value = 'open'
      reconnectAttempt = 0
      while (pending.length > 0) {
        ws.send(pending.shift() as string)
      }
      handlers.onOpen?.()
    }

    ws.onmessage = (event) => {
      // 二进制帧是 M2 的 WebRTC 信令载体，这里只处理文本帧。
      if (typeof event.data !== 'string') return
      try {
        handlers.onMessage(JSON.parse(event.data) as Envelope)
      } catch (err) {
        console.warn('信令报文不是合法 JSON', err, event.data)
      }
    }

    ws.onclose = () => {
      socket.value = null
      status.value = 'closed'
      if (closedByUs) return
      handlers.onClose?.('信令连接已断开')
      scheduleReconnect()
    }

    ws.onerror = () => {
      // 具体原因由随后的 onclose 统一处理，避免两处报错互相覆盖。
    }
  }

  function scheduleReconnect() {
    reconnectAttempt += 1
    const delay = Math.min(1000 * 2 ** (reconnectAttempt - 1), MAX_RECONNECT_DELAY_MS)
    reconnectTimer = window.setTimeout(open, delay)
  }

  /** 发送消息；连接未就绪时先排队，onopen 后按序补发。 */
  function send(env: Envelope) {
    const data = JSON.stringify(env)
    const ws = socket.value
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(data)
      return
    }
    pending.push(data)
  }

  function close() {
    closedByUs = true
    if (reconnectTimer !== undefined) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = undefined
    }
    socket.value?.close(1000, 'client closed')
    socket.value = null
    status.value = 'idle'
  }

  return { status, connect, send, close }
}
