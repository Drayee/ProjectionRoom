/**
 * 用户级实时通道（`/ws/user`）的客户端连接器。wayfinder #31，ACCOUNTS §7.2。
 *
 * # 它做什么
 *
 * 一条 WS 长连接，只承担**下行推送**：新私聊（`dm`）、已读回执（`dm-read`）、
 * 好友事件（`friend-request` / `friend-accepted` / `friend-removed` / `blocked`）、
 * 在线态（`presence`）。所有**写**操作仍走 REST（发消息、已读上报、加好友），
 * 这条通道不接受业务上行 —— 否则同一件事会有两条路径，校验与限速要各写一遍。
 *
 * # 为什么它不在 api/ 门面（mock/real）体系里
 *
 * `mode.ts` 那套开关解决的是"**数据来源**是 mock 还是真实后端"。而"实时推送"
 * 没有 mock 形态可言：mock 态下根本没有服务端在推。所以这里的降级方式是
 * **能力探测**而不是开关：连不上就退化成"靠轮询/刷新拿新数据"，
 * 而那正是这条通道出现之前的行为（界面已经完整可用）。
 *
 * # 票据与重连
 *
 * `/ws/user` 的 ticket 是**单次使用**的（30 秒有效），所以每次重连都必须重新取票
 * （`POST /api/auth/ws-ticket`）。重连用指数退避 + 上限，且**封禁要停止重连**
 * （服务端以 1008 关闭并给出可读原因，见 wsuser.go）。
 */
import { wsTicket } from './auth'
import { decodeEnvelope } from '../types/codec'
import type { DirectMessage, Envelope, FriendEvent, PresenceState } from '../types/protocol'

/** 通道推送的事件（调用方按 `kind` 分派）。 */
export type UserChannelEvent =
  | { kind: 'ready' }
  | { kind: 'dm'; message: DirectMessage }
  | { kind: 'dm-read'; message: DirectMessage }
  | { kind: 'friend'; type: string; event: FriendEvent }
  | { kind: 'presence'; state: PresenceState }

export interface UserChannelHandlers {
  onEvent: (ev: UserChannelEvent) => void
  /** 连接状态变化（供界面显示"实时已连接/已断开"）。 */
  onState?: (state: UserChannelState) => void
}

export type UserChannelState = 'connecting' | 'open' | 'closed' | 'banned'

/** 重连退避：从 1s 起，翻倍到 30s 封顶。 */
const RECONNECT_BASE_MS = 1000
const RECONNECT_MAX_MS = 30_000

/**
 * 用户通道连接器。
 *
 * 用法：
 * ```ts
 * const ch = createUserChannel(handlers)
 * ch.start()   // 登录后调用
 * ch.stop()    // 登出/卸载时调用
 * ```
 */
export interface UserChannel {
  start: () => void
  stop: () => void
  /** 当前是否已连上（界面判断"要不要显示实时徽标"）。 */
  isOpen: () => boolean
}

export function createUserChannel(handlers: UserChannelHandlers): UserChannel {
  let ws: WebSocket | null = null
  let stopped = false
  let attempt = 0
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  /** 封禁之后不再重连（服务端以 1008 + 可读原因关闭）。 */
  let banned = false

  function setState(state: UserChannelState): void {
    handlers.onState?.(state)
  }

  function scheduleReconnect(): void {
    if (stopped || banned || retryTimer !== null) return
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** attempt, RECONNECT_MAX_MS)
    attempt++
    retryTimer = setTimeout(() => {
      retryTimer = null
      void connect()
    }, delay)
  }

  async function connect(): Promise<void> {
    if (stopped || banned) return
    setState('connecting')

    let ticket: string
    try {
      // 每次重连都取新票：票是单次使用的（30s 有效），复用必然 401。
      ticket = (await wsTicketFromStore()).ticket
    } catch {
      // 取票失败（未登录 / 后端未上线 / 网络不可达）：退避重试。
      // 这里**不**区分错误类型 —— 通道是体验增量，任何失败都只该重试或放弃，
      // 不该把错误抛给用户（界面已有"未读徽标 + 手动刷新"这条退路）。
      scheduleReconnect()
      return
    }
    if (stopped || banned) return

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${location.host}/ws/user?ticket=${encodeURIComponent(ticket)}`

    let socket: WebSocket
    try {
      socket = new WebSocket(url)
    } catch {
      scheduleReconnect()
      return
    }
    socket.binaryType = 'arraybuffer'
    ws = socket

    socket.onopen = () => {
      attempt = 0
      setState('open')
    }

    socket.onmessage = (ev: MessageEvent) => {
      if (!(ev.data instanceof ArrayBuffer)) return
      let env: Envelope
      try {
        env = decodeEnvelope(new Uint8Array(ev.data))
      } catch {
        // 解不开的帧直接丢：协议不匹配不该让整条通道崩掉。
        return
      }
      dispatch(env)
    }

    socket.onclose = (ev: CloseEvent) => {
      ws = null
      // 1008 = 策略违规（封禁/票据问题）。封禁时服务端会在 reason 里给出说明，
      // 而重连只会一直被拒 —— 所以这里**停止**重连，让用户去重新登录。
      if (ev.code === 1008) {
        banned = true
        setState('banned')
        return
      }
      setState('closed')
      scheduleReconnect()
    }

    socket.onerror = () => {
      // onerror 之后必然有 onclose（浏览器规范），重连逻辑放在那里统一处理。
    }
  }

  function dispatch(env: Envelope): void {
    switch (env.type) {
      case 'user-ready':
        handlers.onEvent({ kind: 'ready' })
        return
      case 'dm':
        if (env.dm) handlers.onEvent({ kind: 'dm', message: env.dm })
        return
      case 'dm-read':
        if (env.dm) handlers.onEvent({ kind: 'dm-read', message: env.dm })
        return
      case 'friend-request':
      case 'friend-accepted':
      case 'friend-removed':
      case 'blocked':
        if (env.friend) handlers.onEvent({ kind: 'friend', type: env.type, event: env.friend })
        return
      case 'presence':
        if (env.presence) handlers.onEvent({ kind: 'presence', state: env.presence })
        return
      default:
        // 未知类型静默忽略：服务端可能比客户端新（proto3 未知字段/新 type），
        // 而一条不认识的帧不该让整条通道报错。
        return
    }
  }

  return {
    start(): void {
      stopped = false
      banned = false
      attempt = 0
      void connect()
    },
    stop(): void {
      stopped = true
      if (retryTimer !== null) {
        clearTimeout(retryTimer)
        retryTimer = null
      }
      if (ws !== null) {
        ws.onclose = null // 主动关闭不该触发重连
        ws.close(1000, 'client stop')
        ws = null
      }
      setState('closed')
    },
    isOpen(): boolean {
      return ws !== null && ws.readyState === WebSocket.OPEN
    },
  }
}

/**
 * 取一张 WS 票据。
 *
 * 单独包一层是为了让"票从哪来"只有一处：`api/auth.ts` 的 `wsTicket` 需要一个
 * access token，而 token 的唯一持有者是 `stores/auth.ts`。这里**不**直接 import
 * store（那会让 api 层依赖 pinia），而是由调用方在 `start()` 之前把 token 灌进来 ——
 * 见 `useUserChannel` 组合式函数的用法。
 *
 * 当前实现由 `setUserChannelToken` 注入的 token 驱动；未注入时抛错
 * （调用方按"取票失败"退避重试，见 connect 的 catch）。
 */
let currentToken = ''

/** 由 `stores/auth.ts` 在登录/刷新/登出时更新（唯一写入点）。 */
export function setUserChannelToken(token: string): void {
  currentToken = token
}

async function wsTicketFromStore(): Promise<{ ticket: string }> {
  if (currentToken === '') {
    throw new Error('user channel: 没有 access token')
  }
  return wsTicket(currentToken)
}

