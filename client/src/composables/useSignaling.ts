import { ref, shallowRef } from 'vue'
import { decodeEnvelope, encodeEnvelope } from '../types/codec'
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

  const pending: Uint8Array[] = []
  let currentUrl = ''
  let reconnectTimer: number | undefined
  let reconnectAttempt = 0
  let closedByUs = false
  /** 验收钩子：按住期间不发起连接（等价于"这个页面连不上信令"），退避链照常推进。 */
  let holdOpen = false

  function connect(url: string) {
    currentUrl = url
    closedByUs = false
    // 立刻重连的语义：把上一次失败留下的退避定时器清掉，
    // 否则它到点后会再开一条连接（换 clientId / 重建房间路径上就是"新旧两条 WS 并存"）。
    clearReconnectTimer()
    open()
  }

  function clearReconnectTimer() {
    if (reconnectTimer !== undefined) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = undefined
    }
  }

  function open() {
    if (!currentUrl) return

    if (holdOpen) {
      // 验收钩子：被按住 = 这个页面此刻连不上信令。**保持退避重连链**，
      // 这样放开之后的恢复时序与真实断网完全一致（客户端本来就在指数退避重试）。
      status.value = 'closed'
      scheduleReconnect()
      return
    }

    status.value = 'connecting'
    const ws = new WebSocket(currentUrl)
    // 信令报文是 protobuf：按二进制帧接收。
    ws.binaryType = 'arraybuffer'
    socket.value = ws

    ws.onopen = () => {
      // 旧 socket 的迟到事件不能改当前状态：不然会把刚建立好的连接标成 closed。
      if (socket.value !== ws) {
        ws.close(1000, 'stale socket')
        return
      }
      status.value = 'open'
      reconnectAttempt = 0
      while (pending.length > 0) {
        ws.send(pending.shift() as Uint8Array)
      }
      handlers.onOpen?.()
    }

    ws.onmessage = (event) => {
      if (socket.value !== ws) return
      if (!(event.data instanceof ArrayBuffer)) {
        console.warn('信令报文不是二进制帧，已忽略')
        return
      }
      try {
        handlers.onMessage(decodeEnvelope(new Uint8Array(event.data)))
      } catch (err) {
        console.warn('信令报文不是合法 protobuf 信封', err)
      }
    }

    ws.onclose = () => {
      if (socket.value !== ws) return
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
    clearReconnectTimer()
    reconnectAttempt += 1
    const delay = Math.min(1000 * 2 ** (reconnectAttempt - 1), MAX_RECONNECT_DELAY_MS)
    reconnectTimer = window.setTimeout(open, delay)
  }

  /** 发送消息；连接未就绪时先排队，onopen 后按序补发。 */
  function send(env: Envelope) {
    const data = encodeEnvelope(env)
    const ws = socket.value
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(data)
      return
    }
    pending.push(data)
  }

  function close() {
    closedByUs = true
    clearReconnectTimer()
    socket.value?.close(1000, 'client closed')
    socket.value = null
    status.value = 'idle'
  }

  /**
   * 强制掐断当前连接（**验收钩子**）。
   *
   * 与 close() 的区别只有一个：不设 closedByUs，所以这是一次"网络抖动"而不是主动退出 ——
   * onClose 照常触发，退避重连照常进行。CDP 断网在某些 Chrome 版本上不会立刻拆掉
   * 已建立的 WebSocket，验收脚本需要一条确定性的掐线路径。
   */
  function drop() {
    const ws = socket.value
    if (ws) {
      try {
        ws.close()
      } catch {
        // 已经断开就交给 onclose 处理。
      }
      return
    }
    // 还在退避期：直接按"断开"处理并重新排程。
    status.value = 'closed'
    handlers.onClose?.('信令连接已断开')
    scheduleReconnect()
  }

  /**
   * 按住 / 放开信令（**验收钩子**）。
   *
   * 按住 = 立刻断开当前连接，并且之后的每次连接尝试都会被挡下（退避链照常推进）——
   * 等价于"这个页面连不上信令服务"，但网络本身是通的，所以断开是干净的、
   * 服务端能立刻看到（宽限期才会准时开始）。
   * CDP 的 Network.emulateNetworkConditions(offline) 在不少 Chrome 上既不拆已建立的 WS，
   * 还会让 close 帧发不出去，验收脚本需要这条确定性路径。
   *
   * 放开 = 清掉退避定时器并立刻重连（等价于网络恢复后浏览器马上重试）。
   */
  function setHold(hold: boolean) {
    holdOpen = hold
    if (hold) {
      if (socket.value === null) {
        status.value = 'closed'
        handlers.onClose?.('信令连接已断开')
        scheduleReconnect()
      } else {
        drop()
      }
      return
    }
    clearReconnectTimer()
    if (status.value !== 'open') open()
  }

  return { status, connect, send, close, drop, setHold }
}
