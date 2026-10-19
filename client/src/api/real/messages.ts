/**
 * 私聊域 · 真实 HTTP 实现（wayfinder #31 后端落地后填实，#30 切真）。
 *
 * 两条与 `api/real/posts.ts` / `api/real/friends.ts` 一致的写法：一律走
 * `requestJSON(authedFetch, …)`（401 自动刷新一次的语义只在 `stores/auth.ts` 一处实现），
 * 并用 `toCapabilityUnavailable*` 保留"整域 404 = 能力未上线"的判据。
 *
 * 服务端契约（#31 冻结，#30 消费）：
 *
 *   GET    /api/messages?peer=<id>&before=<id>  → {items: messageView[], hasMore}
 *   GET    /api/messages/conversations          → {items: conversationView[]}
 *   POST   /api/messages        {recipientId, body} → messageView（201）
 *   POST   /api/messages/read   {peerId, upTo}      → {updated}
 *   GET    /api/auth/me                          → {user, unread?}
 *
 * 四类形状适配只发生在这一层：
 *   - `senderName` → `sender: PersonRef`（头像派生是纯显示逻辑，服务端契约不掺显示层，
 *     #26 的结论）；
 *   - `mine` 按当前登录用户 id 算 —— 服务端不返回这个字段（它是"相对谁"的视角，
 *     让服务端算等于把当前用户 id 塞进每条消息的响应里）；
 *   - `peerName` → `peer: PersonRef`；
 *   - `unread` 从 `/me` 里取（#31 把它挂在 me 上，省一发专门计数）。
 *
 * ⚠ **404 的两种含义必须分开**（#26 / #28 反复踩过的坑）：
 *   - `GET /api/messages*` 的 404 是"这一组路由没注册" → `CapabilityUnavailable`（整页降级）；
 *   - `POST /api/messages` 的 404 是"收件人不存在" → **语义错误**，必须原地说明。
 *   所以发送走 `toCapabilityUnavailableIf`，用一次会话列表探针回答"域到底在不在"。
 *   否则"给一个已销号的 id 发消息"会被说成"私聊分区还没上线"。
 */
import { requestJSON, query, type AuthedFetch } from '../http'
import { toCapabilityUnavailable, toCapabilityUnavailableIf } from '../mode'
import { avatarOf } from '../../utils/avatar'
import { useAuthStore } from '../../stores/auth'
import type {
  ConversationEntry,
  DirectMessage,
  HistoryPage,
  ListHistoryInput,
  MarkReadInput,
  PersonRef,
  SendMessageInput,
} from '../../types/messages'

/** 取认证 fetch（延迟到调用时：模块加载期 Pinia 可能还没装配）。 */
function fetcher(): AuthedFetch {
  return useAuthStore().authedFetch
}

/**
 * 当前登录用户 id。
 *
 * `profile` 是 localStorage 里的**显示用**缓存（铁律 I3：它不是授权依据），但这里
 * 只用它回答"哪条消息是我发的"—— 一个纯渲染问题。取不到时回落 0，此时所有消息
 * 都会被渲染成"对方的"（`mine=false`）：宁可气泡放错边，也不要因为一个显示问题
 * 让整页报错。
 */
function currentUserId(): number {
  return useAuthStore().profile?.id ?? 0
}

/** 服务端 `messageView`（只声明本文件用到的键）。 */
interface ServerMessage {
  id: number
  kind: string
  senderId: number
  senderName: string
  recipientId?: number
  body: string
  createdAt: string
  readAt: string
}

/** 服务端 `conversationView`。 */
interface ServerConversation {
  peerId: number
  peerName: string
  lastId: number
  lastSenderId: number
  lastBody: string
  lastSentAt: string
  unreadCount: number
}

function toPerson(id: number, displayName: string): PersonRef {
  return { id, displayName: displayName || '未知用户', ...avatarOf(id, displayName) }
}

function toMessage(raw: ServerMessage): DirectMessage {
  return {
    id: raw.id,
    senderId: raw.senderId,
    sender: toPerson(raw.senderId, raw.senderName),
    body: raw.body,
    createdAt: raw.createdAt,
    readAt: raw.readAt ?? '',
    mine: raw.senderId === currentUserId(),
  }
}

function toConversation(raw: ServerConversation): ConversationEntry {
  return {
    peer: toPerson(raw.peerId, raw.peerName),
    lastId: raw.lastId,
    lastSenderId: raw.lastSenderId,
    lastBody: raw.lastBody,
    lastSentAt: raw.lastSentAt,
    unreadCount: raw.unreadCount,
  }
}

/**
 * 会话列表 = 域探针。
 *
 * 它自己的 404 就是"这一组路由没注册"，所以直接用 `toCapabilityUnavailable`
 * （不需要再拿别的探针自证）。页面首屏就是它，整页降级的判据因此只有一处。
 */
export async function listConversations(): Promise<ConversationEntry[]> {
  try {
    const body = await requestJSON<{ items: ServerConversation[] }>(fetcher(), '/api/messages/conversations')
    return (body?.items ?? []).map(toConversation)
  } catch (err) {
    throw toCapabilityUnavailable('messages', err)
  }
}

/**
 * 一页历史（**最新在前**），`before` 是 id 游标。
 *
 * 服务端的 `before` 语义是"只要 id 小于它的"，与 mock 逐条一致；这里**不改顺序**
 * （不做 reverse）：页面渲染时自己倒一次，"往上翻"的边界才只有一处定义。
 */
export async function listHistory(input: ListHistoryInput): Promise<HistoryPage> {
  try {
    const body = await requestJSON<{ items: ServerMessage[]; hasMore: boolean }>(
      fetcher(),
      `/api/messages${query({ peer: input.peerId, before: input.before })}`,
    )
    return { items: (body?.items ?? []).map(toMessage), hasMore: body?.hasMore ?? false }
  } catch (err) {
    // 缺 peer 时服务端回 400（不是 404），所以这里的 404 一定是"域没注册"。
    throw toCapabilityUnavailable('messages', err)
  }
}

/**
 * 发一条私聊，返回服务端落库后的权威行（含自增 id 与 created_at）。
 *
 * 为什么同步返回行而不是 void（与好友域的写操作相反）：这条消息要立刻 append 到
 * 列表里，异步化会逼前端自己编一个临时 id —— 而临时 id 一旦与真实 id 撞上，
 * 后续的"已读上报 upTo"就会把别人刚发来的消息也标掉。这是 #31 的取舍，前端照抄。
 *
 * 403 的两条语义（`MESSAGE_BLOCKED` / `MESSAGE_NOT_FRIENDS`）由服务端给文案，
 * 这里**不预判、不改写**：`requestJSON` 已经把 message 原样带出来。
 */
export async function send(input: SendMessageInput): Promise<DirectMessage> {
  try {
    const body = await requestJSON<ServerMessage>(fetcher(), '/api/messages', {
      method: 'POST',
      body: { recipientId: input.recipientId, body: input.body },
    })
    if (!body) {
      // 201 却读不出行：服务端契约被破坏，不能编一条假消息塞进列表。
      throw new Error('服务端响应缺少消息内容')
    }
    return toMessage(body)
  } catch (err) {
    // 404 在这里是"收件人不存在"（语义错误），必须先用探针确认域还在。
    throw await toCapabilityUnavailableIf('messages', err, () => listConversations())
  }
}

/**
 * 上报已读位置，返回本次真正被标记的行数。
 *
 * 幂等（服务端 `read_at IS NULL` 让重试变 no-op）：页面可以在每次拉到新消息后
 * 无脑上报一次，不必自己记"上次报到哪"。返回值用来决定要不要刷新未读徽标 ——
 * 而不是靠"我以为我标记了"。
 */
export async function markRead(input: MarkReadInput): Promise<number> {
  try {
    const body = await requestJSON<{ updated: number }>(fetcher(), '/api/messages/read', {
      method: 'POST',
      body: { peerId: input.peerId, upTo: input.upTo },
    })
    return body?.updated ?? 0
  } catch (err) {
    throw await toCapabilityUnavailableIf('messages', err, () => listConversations())
  }
}

/**
 * 本文件**不导出取未读数的函数**（wayfinder #30 的取舍）。
 *
 * 未读数挂在 `/me` 上（#31），而它的读取与落地只有一处：`stores/auth.ts` 的
 * `refreshProfile()` / `runBootstrap()` 调 `api.me()` 之后走 `applyServerUnread`。
 * 在这里再包一个 `unreadTotal()` 就等于开第二个写入点，而两个写入点会互相覆盖 ——
 * 真机第一次验收正是这么挂的（顶栏角标时有时无）。所以未读数只从 store 读。
 */
