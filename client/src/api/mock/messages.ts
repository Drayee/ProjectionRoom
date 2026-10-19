/**
 * 私聊域 · 本地 mock（wayfinder #30）。
 *
 * 这里只管"没有后端时怎么跑"：消息存在内存里，写操作改内存并立刻反映到后续读。
 * 页面**不直接** import 本文件 —— 统一走门面 `api/messages.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_MESSAGES` 决定是否用这一份。
 *
 * 五条与真实实现刻意对齐的语义（不然 mock 态验收过了、翻真还会翻车）：
 *
 *  1. **会话是派生视图，不是表**（0012 迁移的结论）：这里也没有 `conversations`
 *     数组，`conversations()` 每次从消息聚合。于是"发一条新消息 → 会话列表顺序变了"
 *     在两份实现里是同一套逻辑，不存在"mock 忘了同步会话行"这种假绿。
 *  2. **未读是收件人的事实**：只有 `recipientId === CURRENT_USER_ID` 的行能被标已读。
 *     发送方看到自己发出的消息永远是未读（`readAt: ''`）—— 与服务端一致。
 *  3. **关系门槛由"服务端"拦**：非好友 403 `MESSAGE_NOT_FRIENDS`、拉黑 403
 *     `MESSAGE_BLOCKED`、自己发给自己 400 `SELF_TARGET`、空正文 400、超长 400。
 *     抛 `HttpError` 而不是裸 `Error`：裸 `Error` 会被页面的分诊压成"网络不可达"，
 *     于是"对方拉黑了你"会显示成"无法连接服务器"（#28 踩过同一个坑）。
 *  4. **历史分页用 id 游标 + 多取一行**：与服务端 `ListDM` 逐条对齐。用 offset
 *     会让"边发边往上翻"重复或漏行。
 *  5. **正文上限 2000 字符**（按 rune 数，不是字节）：与 `store.MaxMessageBodyLen`
 *     同值。不一致的症状是"输入框能打进去、保存报 400"。
 *
 * 在线态 / 实时投递**不在这里**：`/ws/user` 是体验增量，mock 态没有服务端在推，
 * 页面靠"发完重拉一次"就能看到自己的消息（#30 的 out 段明确不做实时投递）。
 */
import { HttpError } from '../http'
import { avatarOf } from '../../utils/avatar'
import type {
  ConversationEntry,
  DirectMessage,
  HistoryPage,
  ListHistoryInput,
  MarkReadInput,
  PersonRef,
  SendMessageInput,
} from '../../types/messages'

/** 当前登录用户 id：与 `api/mock/posts.ts` / `api/mock/friends.ts` 的"我"一致。 */
const CURRENT_USER_ID = 1

/** 正文上限：与 `store.MaxMessageBodyLen` 同值（§7.2 ≤ 2000）。 */
const MAX_BODY = 2000

/** 页大小：与 `store.defaultMessagePage` 同值（§7.2 定死 50）。 */
const PAGE_SIZE = 50

interface MockPerson {
  id: number
  username: string
  displayName: string
}

/**
 * 本地"用户目录"。
 *
 * 刻意覆盖验收要看的四种边界（#30 的验收第 2 条）：
 *   - 2 / 3：好友，有历史 → 会话列表与历史分页的正常路径；
 *   - 5：好友，**没有历史** → "空会话"的空态文案；
 *   - 6：**已拉黑**（对方拉黑我）→ 发送 403 `MESSAGE_BLOCKED` 的文案；
 *   - 7：**不是好友** → 发送 403 `MESSAGE_NOT_FRIENDS` 的文案。
 */
const PEOPLE: MockPerson[] = [
  { id: 2, username: 'projectionist', displayName: '放映员' },
  { id: 3, username: 'azi', displayName: '阿梓' },
  { id: 5, username: 'miao', displayName: '喵喵' },
  { id: 6, username: 'stray_cat', displayName: '路过的猫' },
  { id: 7, username: 'unit07', displayName: '七号机' },
]

/** 好友集合（只有这些能收发私聊，§7.2「先校验关系」）。 */
const FRIEND_IDS = new Set([2, 3, 5])

/**
 * 拉黑集合：`6` 是"对方拉黑了我"。
 *
 * 服务端对拉黑**两个方向都拒**（`ErrMessageBlocked` 的注释说明了理由），所以这里
 * 不必区分方向就能对齐行为；方向只影响文案（服务端文案是"你们之间存在拉黑关系"）。
 */
const BLOCKED_IDS = new Set([6])

interface MockMessage {
  id: number
  senderId: number
  recipientId: number
  body: string
  createdAt: string
  readAt: string
}

let nextMessageId = 1

function nowIso(offsetSeconds = 0): string {
  return new Date(Date.now() - offsetSeconds * 1000).toISOString()
}

/**
 * 种子消息：与 3 有两条（都已读、较早），与 2 有四条（后两条我未读、最新）。
 *
 * id 必须与时间**同序**：服务端的历史分页与"最新会话优先"排序都以 **id** 为序，
 * 用 created_at 排序会在同秒内不稳定。所以这里按"最旧 → 最新"逐条分配 id，
 * 并且时间戳也跟着递减（`nowIso(秒数)` 是"多少秒之前"）。
 *
 * 于是第一个会话（放映员）正好是**有未读的那个** —— 真机验收点第一条就能看到徽标，
 * 不必先猜哪一行有货。
 */
const messages: MockMessage[] = [
  { id: nextMessageId++, senderId: 3, recipientId: CURRENT_USER_ID, body: '你的动效稿我看过了，morph 那段很稳。', createdAt: nowIso(86400), readAt: nowIso(86000) },
  { id: nextMessageId++, senderId: CURRENT_USER_ID, recipientId: 3, body: '谢谢！下次把缓动曲线也发你。', createdAt: nowIso(85000), readAt: nowIso(84500) },
  { id: nextMessageId++, senderId: 2, recipientId: CURRENT_USER_ID, body: '今晚八点那场还开吗？', createdAt: nowIso(7200), readAt: nowIso(7000) },
  { id: nextMessageId++, senderId: CURRENT_USER_ID, recipientId: 2, body: '开，房间码 MOON04。', createdAt: nowIso(6900), readAt: nowIso(6800) },
  { id: nextMessageId++, senderId: 2, recipientId: CURRENT_USER_ID, body: '收到，我带《芙莉莲》第 4 集。', createdAt: nowIso(3600), readAt: '' },
  { id: nextMessageId++, senderId: 2, recipientId: CURRENT_USER_ID, body: '顺便问下能不能带个人来？', createdAt: nowIso(600), readAt: '' },
]

function person(id: number): MockPerson | undefined {
  return PEOPLE.find((p) => p.id === id)
}

/** 把 mock 人物转成页面用的显示形状（头像派生与服务端适配层同一套）。 */
function refOf(p: MockPerson): PersonRef {
  return { id: p.id, displayName: p.displayName, ...avatarOf(p.id, p.displayName) }
}

/**
 * 未知用户的退化形状。
 *
 * 真实实现里查不到人只可能是"对方已销号"，但 mock 的种子允许残行；把一个可见的
 * 占位人塞进模板，比把 `undefined` 漏进渲染树好。
 */
function unknownPerson(id: number): PersonRef {
  return { id, displayName: '未知用户', ...avatarOf(id, '') }
}

function toMessage(row: MockMessage): DirectMessage {
  const p = person(row.senderId)
  return {
    id: row.id,
    senderId: row.senderId,
    sender: p ? refOf(p) : unknownPerson(row.senderId),
    body: row.body,
    createdAt: row.createdAt,
    readAt: row.readAt,
    mine: row.senderId === CURRENT_USER_ID,
  }
}

function conflict(status: number, code: string, message: string): HttpError {
  return new HttpError({ status, code, message })
}

// ---------- 导出的 API（形状必须与 api/real/messages.ts 完全一致）----------

/**
 * 会话列表（每个对话一行，按最近消息倒序）。
 *
 * 聚合方式与服务端 `ListDMConversations` 同构：先把"我参与的 dm"归一成
 * `(peerId, row)`，每个 peer 取 id 最大的那条，再按该 id 倒序；未读数按 peer 现算。
 */
export async function listConversations(): Promise<ConversationEntry[]> {
  const mine = messages.filter((m) => m.senderId === CURRENT_USER_ID || m.recipientId === CURRENT_USER_ID)

  const latestByPeer = new Map<number, MockMessage>()
  for (const m of mine) {
    const peerId = m.senderId === CURRENT_USER_ID ? m.recipientId : m.senderId
    const prev = latestByPeer.get(peerId)
    if (!prev || m.id > prev.id) latestByPeer.set(peerId, m)
  }

  const rows = [...latestByPeer.entries()].map(([peerId, last]) => {
    const p = person(peerId)
    return {
      peer: p ? refOf(p) : unknownPerson(peerId),
      lastId: last.id,
      lastSenderId: last.senderId,
      lastBody: last.body,
      lastSentAt: last.createdAt,
      unreadCount: mine.filter((m) => m.recipientId === CURRENT_USER_ID && m.senderId === peerId && m.readAt === '').length,
    }
  })
  rows.sort((a, b) => b.lastId - a.lastId)
  return rows
}

/**
 * 一页历史（**最新在前**），`before` 是 id 游标。
 *
 * 只返回 `kind=dm` 的形态在这里是天然的（mock 里没有房间消息），但顺序、游标语义、
 * "多取一行判 hasMore"这三条与 `store.ListDM` 逐条对齐 —— 它们才是翻页出错的地方。
 */
export async function listHistory(input: ListHistoryInput): Promise<HistoryPage> {
  const { peerId, before } = input
  if (!Number.isInteger(peerId) || peerId <= 0) {
    throw conflict(400, 'BAD_REQUEST', 'peer 必填且必须是正整数')
  }
  const rows = messages
    .filter(
      (m) =>
        (m.senderId === CURRENT_USER_ID && m.recipientId === peerId) ||
        (m.senderId === peerId && m.recipientId === CURRENT_USER_ID),
    )
    .filter((m) => (before && before > 0 ? m.id < before : true))
    .sort((a, b) => b.id - a.id)

  const page = rows.slice(0, PAGE_SIZE)
  return { items: page.map(toMessage), hasMore: rows.length > PAGE_SIZE }
}

/**
 * 发一条私聊，返回落库后的权威行。
 *
 * 校验顺序与服务端 `SendDM` 一致：自己 → 收件人存在 → 拉黑 → 好友 → 正文非空 → 长度。
 * 顺序是有意义的：先判"不能发给自己"再判关系，用户得到的永远是**最先命中的那条**原因，
 * 两份实现给出同一句话。
 */
export async function send(input: SendMessageInput): Promise<DirectMessage> {
  const recipientId = input.recipientId
  if (!Number.isInteger(recipientId) || recipientId <= 0) {
    throw conflict(400, 'BAD_REQUEST', 'recipientId 必填且必须是正整数')
  }
  if (recipientId === CURRENT_USER_ID) {
    throw conflict(400, 'SELF_TARGET', '不能给自己发私聊')
  }
  if (!person(recipientId)) {
    throw conflict(404, 'NOT_FOUND', '目标用户不存在')
  }
  if (BLOCKED_IDS.has(recipientId)) {
    throw conflict(403, 'MESSAGE_BLOCKED', '你们之间存在拉黑关系，无法发送私聊')
  }
  if (!FRIEND_IDS.has(recipientId)) {
    throw conflict(403, 'MESSAGE_NOT_FRIENDS', '只能给好友发私聊')
  }

  const body = input.body.trim()
  if (body === '') {
    throw conflict(400, 'BAD_REQUEST', '消息内容不能为空')
  }
  if ([...body].length > MAX_BODY) {
    throw conflict(400, 'BAD_REQUEST', `消息内容不能超过 ${MAX_BODY} 个字符`)
  }

  const row: MockMessage = {
    id: nextMessageId++,
    senderId: CURRENT_USER_ID,
    recipientId,
    body,
    createdAt: new Date().toISOString(),
    readAt: '',
  }
  messages.push(row)
  return toMessage(row)
}

/**
 * 上报"读到哪一条"，返回本次真正被标记的行数。
 *
 * 两条与 `MarkDMRead` 一致的判据：
 *   - **只动 `recipientId === CURRENT_USER_ID` 的行**（已读是收件人的事实）；
 *   - **幂等**：已读的行不再计入，所以重复上报返回 0。
 *
 * `upTo` 缺省**不能**当成"全部已读"（服务端对 <=0 回 400）：这里同样 400，
 * 让"少传一个参数"在两份实现里是同一种失败。
 */
export async function markRead(input: MarkReadInput): Promise<number> {
  const { peerId, upTo } = input
  if (!Number.isInteger(peerId) || peerId <= 0) {
    throw conflict(400, 'BAD_REQUEST', 'peerId 必填且必须是正整数')
  }
  if (!Number.isInteger(upTo) || upTo <= 0) {
    throw conflict(400, 'BAD_REQUEST', 'upTo 必填且必须是正整数（缺省不代表全部已读）')
  }
  let updated = 0
  for (const m of messages) {
    if (m.recipientId === CURRENT_USER_ID && m.senderId === peerId && m.id <= upTo && m.readAt === '') {
      m.readAt = new Date().toISOString()
      updated++
    }
  }
  return updated
}

/**
 * 未读数**不在这里提供**（wayfinder #30 的取舍）。
 *
 * 它的唯一来源是 `stores/auth.ts` 的 `profile.unread`，两个写入点：
 *   - 会话列表的合计（`listConversations()` 的 `unreadCount` 之和）—— mock 与 real 都走这条；
 *   - real 域额外接受 `/me` 的 `unread`（服务端按部分索引现算的权威值）。
 *
 * 如果这里再导出一个 `unreadTotal()`，它就会成为**第三个**来源：三个数字都往同一个
 * ref 里写，谁后跑谁赢。第一次真机验收正是这么挂的 —— mock 的假消息配上真后端的
 * `/me`（`unread: 0`），顶栏角标时有时无。所以这里只留一条：会话列表算。
 */
