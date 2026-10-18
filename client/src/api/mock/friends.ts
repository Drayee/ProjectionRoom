/**
 * 好友域 · 本地 mock（wayfinder #28）。
 *
 * 这里只管"没有后端时怎么跑"：关系存在内存里，写操作改内存并立刻反映到后续读。
 * 页面**不直接** import 本文件 —— 统一走门面 `api/friends.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_FRIENDS` 决定是否用这一份。
 *
 * 三条与真实实现刻意对齐的语义（不然 mock 态验收过了、翻真还会翻车）：
 *
 *  1. **"一段关系只存一行"**（docs/ACCOUNTS.md §7.1）：accepted 对称、pending 有向、
 *     blocked 有向（`blockedById` 记谁拉黑了谁）。所以 A、B 之间不会同时存在两条边。
 *  2. **删除/接受/拒绝按 `relationshipId`**，不是对方 userId；与服务端路由参数一致。
 *  3. **冲突走 HttpError（409/403/400），文案与真实实现逐字一致**。
 *     抛裸 `Error` 会被页面的分诊压成"网络不可达"（`toHttpError` 的兜底），
 *     于是"已经是好友"会被显示成"无法连接服务器" —— 那是个会误导排查的假象。
 *
 * 在线状态（`presence`）是本文件**唯一**没有真实对应物的字段：真实实现恒为
 * `'unknown'`，等后续图的 `/ws/user` 实时通道。这里给的假数据只为了让"这一列将来长
 * 什么样"能在 mock 态看见。
 */
import { HttpError } from '../http'
import { avatarOf } from '../../utils/avatar'
import type {
  BlockedEntry,
  FriendEntry,
  FriendRequest,
  FriendRequests,
  PresenceState,
  RelationState,
  SearchHit,
} from '../../types/friends'

/** 当前登录用户 id：与 `api/mock/posts.ts` 的 mock 当前用户一致（同一个"我"）。 */
const CURRENT_USER_ID = 1

interface MockPerson {
  id: number
  /** 登录名（搜索按它与昵称一起匹配，真实接口将来也是这两列）。 */
  username: string
  displayName: string
  presence: PresenceState
}

/** 本地"用户目录"：只有出现在这里的人才能被搜到/加好友。 */
const PEOPLE: MockPerson[] = [
  { id: 1, username: 'mooncat', displayName: '月夜猫', presence: 'online' },
  { id: 2, username: 'projectionist', displayName: '放映员', presence: 'online' },
  { id: 3, username: 'azi', displayName: '阿梓', presence: 'offline' },
  { id: 4, username: 'nightowl', displayName: '夜猫子', presence: 'online' },
  { id: 5, username: 'miao', displayName: '喵喵', presence: 'online' },
  { id: 6, username: 'stray_cat', displayName: '路过的猫', presence: 'unknown' },
  { id: 7, username: 'unit07', displayName: '七号机', presence: 'offline' },
  { id: 8, username: 'xiaoman', displayName: '小满', presence: 'online' },
]

interface MockRelation {
  relationshipId: number
  requesterId: number
  addresseeId: number
  status: 'pending' | 'accepted' | 'blocked'
  /** 拉黑方（只有 status==='blocked' 时有意义），与 `friendships.blocked_by_id` 同义。 */
  blockedById?: number
}

let nextRelationshipId = 1001

/**
 * 种子数据：覆盖验收要看的四种状态各一条。
 *
 * 刻意让"待处理"两摞都有货（收件箱一条、发件箱一条），因为界面在这两种行上能做的
 * 动作完全不同：收件箱可接受/拒绝，发件箱**只能等**（服务端没有"撤回请求"端点）。
 */
const relations: MockRelation[] = [
  { relationshipId: nextRelationshipId++, requesterId: 1, addresseeId: 2, status: 'accepted' },
  { relationshipId: nextRelationshipId++, requesterId: 3, addresseeId: 1, status: 'accepted' },
  { relationshipId: nextRelationshipId++, requesterId: 4, addresseeId: 1, status: 'pending' },
  { relationshipId: nextRelationshipId++, requesterId: 1, addresseeId: 5, status: 'pending' },
  { relationshipId: nextRelationshipId++, requesterId: 6, addresseeId: 1, status: 'blocked', blockedById: 1 },
]

function person(id: number): MockPerson | undefined {
  return PEOPLE.find((p) => p.id === id)
}

/** 把 mock 人物转成页面用的显示形状（头像派生与服务端适配层同一套）。 */
function refOf(p: MockPerson) {
  return { id: p.id, displayName: p.displayName, ...avatarOf(p.id, p.displayName) }
}

function otherId(rel: MockRelation): number {
  return rel.requesterId === CURRENT_USER_ID ? rel.addresseeId : rel.requesterId
}

/** 我和某人之间那唯一的一行（任一方向）。 */
function relationBetween(userId: number): MockRelation | undefined {
  return relations.find(
    (r) =>
      (r.requesterId === CURRENT_USER_ID && r.addresseeId === userId) ||
      (r.requesterId === userId && r.addresseeId === CURRENT_USER_ID),
  )
}

function relationStateOf(userId: number): RelationState {
  const rel = relationBetween(userId)
  if (!rel) return 'none'
  if (rel.status === 'accepted') return 'friend'
  if (rel.status === 'blocked') return 'blocked'
  return rel.requesterId === CURRENT_USER_ID ? 'pending-out' : 'pending-in'
}

function conflict(status: number, code: string, message: string): HttpError {
  return new HttpError({ status, code, message })
}

// ---------- 导出的 API（形状必须与 api/real/friends.ts 完全一致）----------

/** 好友列表（accepted）。服务端按最近变更倒序，这里保持数组顺序（新的在前）。 */
export async function listFriends(): Promise<FriendEntry[]> {
  return relations
    .filter((r) => r.status === 'accepted' && (r.requesterId === CURRENT_USER_ID || r.addresseeId === CURRENT_USER_ID))
    .map((r) => {
      const id = otherId(r)
      const p = person(id)
      return {
        relationshipId: r.relationshipId,
        // 人物目录里查不到（真实实现里不可能，但 mock 的种子允许这种残行）：
        // 退化成一个可见的占位人，而不是把 undefined 塞进模板。
        user: p ? refOf(p) : { id, displayName: '未知用户', ...avatarOf(id, '') },
        status: 'accepted' as const,
        presence: p?.presence ?? 'unknown',
      }
    })
}

/** 待处理请求：收件箱（我能接受/拒绝）与发件箱（我只能等）。 */
export async function listRequests(): Promise<FriendRequests> {
  const row = (relationshipId: number, userId: number): FriendRequest => {
    const p = person(userId)
    return {
      relationshipId,
      user: p ? refOf(p) : { id: userId, displayName: '未知用户', ...avatarOf(userId, '') },
      status: 'pending',
    }
  }
  const incoming = relations
    .filter((r) => r.status === 'pending' && r.addresseeId === CURRENT_USER_ID)
    .map((r) => row(r.relationshipId, r.requesterId))
  const outgoing = relations
    .filter((r) => r.status === 'pending' && r.requesterId === CURRENT_USER_ID)
    .map((r) => row(r.relationshipId, r.addresseeId))
  return { incoming, outgoing }
}

/** 按用户名/昵称找人。空查询返回空数组（页面在本地也会拦，这里兜一道）。 */
export async function searchUsers(query: string): Promise<SearchHit[]> {
  const q = query.trim().toLowerCase()
  if (q === '') return []
  return PEOPLE.filter((p) => p.id !== CURRENT_USER_ID)
    .filter((p) => p.username.toLowerCase().includes(q) || p.displayName.toLowerCase().includes(q))
    .map((p) => ({ ...refOf(p), relation: relationStateOf(p.id) }))
}

/** 发起好友请求。裁决顺序与服务端 `CreateFriendshipRequest` 一致（先看已有关系再落行）。 */
export async function sendRequest(userId: number): Promise<void> {
  if (userId === CURRENT_USER_ID) throw conflict(400, 'SELF_RELATION', '不能对自己建立关系')
  if (!person(userId)) throw conflict(404, 'NOT_FOUND', '用户不存在')
  const existing = relationBetween(userId)
  if (existing) {
    if (existing.status === 'accepted') throw conflict(409, 'FRIENDSHIP_CONFLICT', '已经是好友')
    if (existing.status === 'pending') throw conflict(409, 'FRIENDSHIP_CONFLICT', '已有待处理的好友请求')
    // blocked：方向决定文案 —— 是我拉黑了对方，还是对方拉黑了我。
    throw existing.blockedById === CURRENT_USER_ID
      ? conflict(403, 'YOU_BLOCKED_TARGET', '你已拉黑对方，请先解除拉黑')
      : conflict(403, 'BLOCKED_BY_TARGET', '对方已将你拉黑')
  }
  relations.push({
    relationshipId: nextRelationshipId++,
    requesterId: CURRENT_USER_ID,
    addresseeId: userId,
    status: 'pending',
  })
}

/** 接受请求。只有 addressee 能接受（有向语义），否则与服务端一样回 404（不暴露存在性）。 */
export async function acceptRequest(relationshipId: number): Promise<void> {
  const rel = relations.find((r) => r.relationshipId === relationshipId)
  if (!rel || rel.status !== 'pending' || rel.addresseeId !== CURRENT_USER_ID) {
    throw conflict(404, 'NOT_FOUND', '好友关系不存在')
  }
  rel.status = 'accepted'
}

/** 拒绝请求：删掉 pending 行。同样只有 addressee 能拒绝。 */
export async function rejectRequest(relationshipId: number): Promise<void> {
  const idx = relations.findIndex(
    (r) => r.relationshipId === relationshipId && r.status === 'pending' && r.addresseeId === CURRENT_USER_ID,
  )
  if (idx === -1) throw conflict(404, 'NOT_FOUND', '好友关系不存在')
  relations.splice(idx, 1)
}

/** 删除好友（accepted → 删除）。双方都能删。 */
export async function removeFriend(relationshipId: number): Promise<void> {
  const idx = relations.findIndex(
    (r) =>
      r.relationshipId === relationshipId &&
      r.status === 'accepted' &&
      (r.requesterId === CURRENT_USER_ID || r.addresseeId === CURRENT_USER_ID),
  )
  if (idx === -1) throw conflict(404, 'NOT_FOUND', '好友关系不存在')
  relations.splice(idx, 1)
}

/**
 * 拉黑某人。
 *
 * **覆盖既有关系**（与服务端 `BlockUser` 一致）：之前是 pending 还是 accepted，
 * 拉黑后那唯一一行原地变成 blocked，于是"好友请求被拒、列表互不可见"自然成立 ——
 * 不需要额外清理动作，也不会留下两行互相矛盾的记录。
 */
export async function blockUser(userId: number): Promise<void> {
  if (userId === CURRENT_USER_ID) throw conflict(400, 'SELF_RELATION', '不能对自己建立关系')
  const existing = relationBetween(userId)
  if (existing) {
    existing.status = 'blocked'
    existing.blockedById = CURRENT_USER_ID
    return
  }
  relations.push({
    relationshipId: nextRelationshipId++,
    requesterId: CURRENT_USER_ID,
    addresseeId: userId,
    status: 'blocked',
    blockedById: CURRENT_USER_ID,
  })
}

/** 黑名单：只列**我拉黑的**人（对方拉黑我的那一行，服务端也不该让我看见）。 */
export async function listBlocked(): Promise<BlockedEntry[]> {
  return relations
    .filter((r) => r.status === 'blocked' && r.blockedById === CURRENT_USER_ID)
    .map((r) => {
      const id = otherId(r)
      const p = person(id)
      return { user: p ? refOf(p) : { id, displayName: '未知用户', ...avatarOf(id, '') } }
    })
}

/** 解除拉黑：删掉那一行（关系回到"没有关系"，不是回到好友）。 */
export async function unblock(userId: number): Promise<void> {
  const idx = relations.findIndex(
    (r) => r.status === 'blocked' && r.blockedById === CURRENT_USER_ID && otherId(r) === userId,
  )
  if (idx === -1) throw conflict(404, 'NOT_FOUND', '黑名单里没有这个人')
  relations.splice(idx, 1)
}
