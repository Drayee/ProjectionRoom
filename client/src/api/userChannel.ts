/**
 * 用户级实时通道（`/ws/user`）的客户端连接器。wayfinder #31，ACCOUNTS §7.2。
 *
 * # 它做什么
 *
 * 一条 WS 长连接，只承担**下行推送**：新私聊（`dm`）、已读回执（`dm-read`）、
 * 好友事件（`friend-request` / `friend-accepted` / `friend-removed` / `blocked`）、
 * 关注事件（`follow-created` / `mutual-follow`，票 #67）、
 * 在线态（`presence`）。所有**写**操作仍走 REST（发消息、已读上报、加关注），
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
 * （`POST /api/auth/ws-ticket`）。三种"停下来"的理由各不相同，别合并：
 *
 *   - **封禁**：服务端以 1008 + 可读原因关闭。新 token 也没用，只能等用户重新登录
 *     或申诉（`banned`，且 `start()` 会清掉它 —— 重新登录就是一次新的 `start()`）；
 *   - **凭据失效**：取票回 401 / 本机没有 token，或握手被 403 拒（Origin 白名单没配
 *     这个域名）。**重连没有意义**：配置与凭据都没变，重连一万次结果一样，代价却是
 *     控制台里刷屏的失败请求 + 每个周期一次多余的取票（`suspended`）。
 *     解除条件是"出现了新凭据" → `setUserChannelToken` 唤醒（见文件尾部）；
 *   - **瞬时故障**：网络不可达、后端未上线、5xx。这些会自己好，指数退避重试是对的。
 *
 * 2026-11-02 线上事故就是缺了第二类：`PR_ALLOWED_ORIGINS` 没配新域名，于是每次握手
 * 都 403；而当时的实现把 403 当瞬时故障无限重连，于是几十条
 * `WebSocket connection to 'ws://…/ws/user?ticket=…' failed: …403` 刷满控制台。
 */
import { AuthApiError, toAuthError, wsTicket } from './auth'
import { decodeEnvelope } from '../types/codec'
import { isCredentialFailure } from '../utils/channelRetry'
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

export type UserChannelState = 'connecting' | 'open' | 'closed' | 'banned' | 'suspended'

/** 重连退避：从 1s 起，翻倍到 30s 封顶。 */
const RECONNECT_BASE_MS = 1000
const RECONNECT_MAX_MS = 30_000

/**
 * 这次取票失败是不是"凭据/配置问题"（应当挂起等新 token，而不是退避重连）。
 *
 * 判据本体在 `utils/channelRetry.ts`（零依赖纯模块，因此进得了 `client/test/`
 * 的表驱动用例）。这里只做一层薄适配：把 `AuthApiError` 归一后交给它。
 *
 * 为什么 403 也算"别重连"：2026-11-02 线上那次，403 来自 Origin 白名单没配新域名。
 * 配置不改，重连一万次结果一样，而每次重连都要先取一张票 —— 于是控制台里就成了
 * "一串 `api/auth/ws-ticket` 401 + 一串 `/ws/user` 403"。
 *
 * 反过来说，网络不可达 / 5xx / 后端未上线**必须**继续退避重试：那些是会自己好的。
 */
function shouldSuspend(err: unknown): boolean {
  return isCredentialFailure(toAuthError(err))
}

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
  /**
   * 拿到新凭据后唤醒挂起的通道。
   *
   * 只在"因为凭据失效而挂起"时才真正重连；其余情况是空操作，所以调用方
   * （`setUserChannelToken`）不需要自己判断通道当前的内部状态。
   */
  resume: () => void
}

export function createUserChannel(handlers: UserChannelHandlers): UserChannel {
  let ws: WebSocket | null = null
  let stopped = false
  let attempt = 0
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  /** 封禁之后不再重连（服务端以 1008 + 可读原因关闭）。 */
  let banned = false
  /**
   * 凭据失效而**挂起**：不再重连，等 `setUserChannelToken` 注入新 token 后唤醒。
   *
   * 与 `banned` 分开是因为两者的解除条件不同：封禁要用户重新登录（新 token 也没用，
   * 服务端会一直 1008），而凭据失效只要换一枚 token 就该立刻能连上。
   */
  let suspended = false

  function setState(state: UserChannelState): void {
    handlers.onState?.(state)
  }

  function scheduleReconnect(): void {
    if (stopped || banned || suspended || retryTimer !== null) return
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** attempt, RECONNECT_MAX_MS)
    attempt++
    retryTimer = setTimeout(() => {
      retryTimer = null
      void connect()
    }, delay)
  }

  async function connect(): Promise<void> {
    if (stopped || banned || suspended) return
    setState('connecting')

    let ticket: string
    try {
      // 每次重连都取新票：票是单次使用的（30s 有效），复用必然 401。
      ticket = (await wsTicketFromStore()).ticket
    } catch (err) {
      if (shouldSuspend(err)) {
        // 挂起：不排重连。等 `setUserChannelToken`（登录/刷新会话后由 stores/auth.ts
        // 唯一调用）注入新 token 时再 `resume()`。
        suspended = true
        setState('suspended')
        return
      }
      scheduleReconnect()
      return
    }
    if (stopped || banned || suspended) return

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
      // 关注事件（票 #67；决议 #53 §5）：复用 friend 帧的形状，
      // 客户端按 `type` 分派。两种类型各有各的文案与落点，见
      // `utils/followNotice.ts` —— 那里是**唯一**的文案源，真机脚本也读它。
      case 'follow-created':
      case 'mutual-follow':
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

  // 先绑定再返回：`start()`/`stop()` 里要引用 `channel` 本身（登记/注销到
  // `liveChannels`），直接 `return { … }` 时那个标识符还不存在。
  const channel: UserChannel = {
    start(): void {
      stopped = false
      banned = false
      attempt = 0
      // 显式 `start()` 是"现在就该连"的意愿表达（登录、用户点重试），
      // 所以它同时解除挂起；否则一次 401 之后登录成功也连不上。
      suspended = false
      // 登记进全局表：挂起后要靠 `setUserChannelToken` 唤醒。
      liveChannels.add(channel)
      void connect()
    },
    stop(): void {
      stopped = true
      suspended = false
      liveChannels.delete(channel)
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
    resume(): void {
      // 只有挂起着才需要动：正常重连中的通道会自己按退避再试，从外面再插一次
      // 会让两条重连路径并存（等待中的 timer + 这次直连）。
      if (!suspended || stopped || banned) return
      suspended = false
      attempt = 0
      if (retryTimer !== null) {
        clearTimeout(retryTimer)
        retryTimer = null
      }
      void connect()
    },
    isOpen(): boolean {
      return ws !== null && ws.readyState === WebSocket.OPEN
    },
  }
  return channel
}

/**
 * 取一张 WS 票据。
 *
 * 单独包一层是为了让"票从哪来"只有一处：`api/auth.ts` 的 `wsTicket` 需要一个
 * access token，而 token 的唯一持有者是 `stores/auth.ts`。这里**不**直接 import
 * store（那会让 api 层依赖 pinia），而是由调用方在 `start()` 之前把 token 灌进来 ——
 * 见 `useUserChannel` 组合式函数的用法。
 *
 * 当前实现由 `setUserChannelToken` 注入的 token 驱动；未注入时抛 `AUTH_NO_TOKEN`
 * （`connect` 据此**挂起**而不是退避重连，见 `utils/channelRetry.ts` 的判据）。
 */
let currentToken = ''

/**
 * 已经创建出来、还没 `stop()` 的通道。
 *
 * 为什么要有这张表：挂起的通道必须能被**新 token 唤醒**，而 token 的唯一写入口是
 * 下面的 `setUserChannelToken`（`stores/auth.ts` 登录/刷新/登出时调）。没有这张表，
 * "拿到新凭据后自动恢复"就只能靠调用方自己记得再调一次 `start()` —— 那正是
 * 本文件 2026-11-02 之前的状态：`runBootstrap` 里 `syncChannelToken()` 注入了 token，
 * 但挂起的通道永远等不到唤醒。
 */
const liveChannels = new Set<UserChannel>()

/**
 * 由 `stores/auth.ts` 在登录/刷新/登出时更新（**唯一**写入点）。
 *
 * 顺带唤醒所有因凭据失效而挂起的通道：新 token 到了，它们就该重连。
 * 注入空串（登出）时不唤醒 —— 那时通道会被 `stop()`，没有重连的意义。
 */
export function setUserChannelToken(token: string): void {
  currentToken = token
  if (token === '') return
  for (const ch of liveChannels) ch.resume()
}

async function wsTicketFromStore(): Promise<{ ticket: string }> {
  if (currentToken === '') {
    // 专门的自造码：`createUserChannel` 据此判"该挂起等新 token"而不是退避重连。
    // 用普通 Error 会让它落进"瞬时故障"分支，于是未登录时也会一直空转。
    throw new AuthApiError({
      status: 0,
      code: 'AUTH_NO_TOKEN',
      message: 'user channel: 没有 access token',
    })
  }
  return wsTicket(currentToken)
}

