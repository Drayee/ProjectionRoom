/**
 * 好友域 · 真实 HTTP 实现（wayfinder #28；服务端契约由 #29 冻结）。
 *
 * 两条与 `api/admin.ts` / `api/real/posts.ts` 一致的写法：一律走
 * `requestJSON(authedFetch, …)`（401 自动刷新一次的语义只在 `stores/auth.ts` 一处实现），
 * 并用 `toCapabilityUnavailable*` 保留"整域 404 = 能力未上线"的判据。
 *
 * 服务端契约（#29 已冻结，6 个端点，全部要登录）：
 *
 *   GET    /api/friends                            → {items: friendshipView[]}     （accepted）
 *   GET    /api/friends/requests                    → {incoming, outgoing}          （pending 两摞）
 *   POST   /api/friends/requests       {userId}     → friendshipView（201）
 *   POST   /api/friends/requests/:requestId/accept  → friendshipView
 *   POST   /api/friends/requests/:requestId/reject  → 204
 *   DELETE /api/friends/:friendshipId               → 204
 *   POST   /api/blocks                 {userId}     → friendshipView
 *
 * `friendshipView` = `{relationshipId, userId, displayName, status}`，其中 `userId` 是
 * **对方**。三处形状适配只发生在这一层：
 *   - `userId + displayName` → `user: {id, displayName, avatarChar, avatarColor}`
 *     （头像派生是纯显示逻辑，服务端契约不掺显示层 —— #26 的结论）；
 *   - `presence` 恒为 `'unknown'`：presence 要等后续图的 `/ws/user`；这里**不猜**；
 *   - 写操作只回一行、且**没有 displayName**（create/accept 的响应只带 userId），
 *     所以门面把它们的返回值定成 `void`，页面写完重新拉列表 —— 权威显示名来自列表，
 *     不是从响应里硬拼一个空名字。
 *
 * ⚠ **三处服务端还没有的能力**（本文件显式抛 `FeatureUnavailable`，页面在**原地**
 * 说明"这个接口还没有"，而不是把整页降级成"该分区未上线"）：
 *   1. 按用户名/昵称找人（`GET /api/users/search?q=`）—— 没有它，"加好友"只能靠
 *      userId，而 userId 在界面上根本不可得；
 *   2. 黑名单列表（`GET /api/blocks`）—— 没有它，只能拉黑、无法回看；
 *   3. 解除拉黑（`DELETE /api/blocks/:userId`）—— 没有它，拉黑是**单向不可逆**的。
 * 三者已作为一张后续后端票提出（见 #28 的结论）。
 *
 * ⚠ **404 的两种含义必须分开**（#26 踩过的坑）：`accept` / `reject` / `remove` 的 404
 * 是**语义**错误——服务端对"关系不存在"与"越权"统一回 404（#29 的防探测决定），它跟
 * "整域路由没注册"长得一模一样。所以这三个走 `toCapabilityUnavailableIf`，用一次
 * `listFriends()` 探针回答"域到底在不在"；否则"这条请求已被处理"会被说成
 * "后端没上线"，用户会以为好友功能全挂了。
 */
import { requestJSON, type AuthedFetch } from '../http'
import { FeatureUnavailable, toCapabilityUnavailable, toCapabilityUnavailableIf } from '../mode'
import { avatarOf } from '../../utils/avatar'
import { useAuthStore } from '../../stores/auth'
import type {
  BlockedEntry,
  FriendEntry,
  FriendRequest,
  FriendRequests,
  SearchHit,
  FriendshipStatus,
  PresenceState,
} from '../../types/friends'

/** presence 的唯一合法取值：等 `/ws/user`（后续图）。绝不按"最后活跃时间"之类猜。 */
const PRESENCE_UNKNOWN: PresenceState = 'unknown'

/** 取认证 fetch（延迟到调用时：模块加载期 Pinia 可能还没装配）。 */
function fetcher(): AuthedFetch {
  return useAuthStore().authedFetch
}

/** 服务端 `friendshipView`。`displayName` 在 create/accept 的响应里是空串。 */
interface ServerFriendship {
  relationshipId: number
  userId: number
  displayName: string
  status: string
}

function toStatus(raw: string): FriendshipStatus {
  return raw === 'accepted' || raw === 'blocked' ? raw : 'pending'
}

function toPerson(userId: number, displayName: string) {
  return { id: userId, displayName: displayName || '未知用户', ...avatarOf(userId, displayName) }
}

function toFriendEntry(raw: ServerFriendship): FriendEntry {
  return {
    relationshipId: raw.relationshipId,
    user: toPerson(raw.userId, raw.displayName),
    status: toStatus(raw.status),
    presence: PRESENCE_UNKNOWN,
  }
}

function toRequest(raw: ServerFriendship): FriendRequest {
  return {
    relationshipId: raw.relationshipId,
    user: toPerson(raw.userId, raw.displayName),
    status: toStatus(raw.status),
  }
}

/**
 * 好友列表 = 域探针。
 *
 * 它自己的 404 就是"这一组路由没注册"，所以直接用 `toCapabilityUnavailable`
 * （不需要再拿别的探针自证）。
 */
export async function listFriends(): Promise<FriendEntry[]> {
  try {
    const body = await requestJSON<{ items: ServerFriendship[] }>(fetcher(), '/api/friends')
    return (body?.items ?? []).map(toFriendEntry)
  } catch (err) {
    throw toCapabilityUnavailable('friends', err)
  }
}

export async function listRequests(): Promise<FriendRequests> {
  try {
    const body = await requestJSON<{ incoming: ServerFriendship[]; outgoing: ServerFriendship[] }>(
      fetcher(),
      '/api/friends/requests',
    )
    return {
      incoming: (body?.incoming ?? []).map(toRequest),
      outgoing: (body?.outgoing ?? []).map(toRequest),
    }
  } catch (err) {
    throw toCapabilityUnavailable('friends', err)
  }
}

/** 按用户名找人：服务端没有这个接口（见文件头的三处缺口），显式说到底缺什么。 */
export async function searchUsers(_query: string): Promise<SearchHit[]> {
  throw new FeatureUnavailable('friends', '按用户名找人')
}

/**
 * 发起好友请求。
 *
 * 语义失败由服务端给：409 `FRIENDSHIP_CONFLICT`（已经是好友 / 已有待处理请求）、
 * 403 `BLOCKED_BY_TARGET`（被对方拉黑）。这里**不预判、不改写文案**——
 * `requestJSON` 已经把服务端 message 原样带出来，页面直接显示。
 */
export async function sendRequest(userId: number): Promise<void> {
  try {
    await requestJSON<ServerFriendship>(fetcher(), '/api/friends/requests', {
      method: 'POST',
      body: { userId },
    })
  } catch (err) {
    throw toCapabilityUnavailable('friends', err)
  }
}

export async function acceptRequest(relationshipId: number): Promise<void> {
  try {
    await requestJSON<ServerFriendship>(fetcher(), `/api/friends/requests/${relationshipId}/accept`, {
      method: 'POST',
    })
  } catch (err) {
    // 404 在这里是语义错误（"请求已被处理/不存在"），必须先用探针确认域还在。
    throw await toCapabilityUnavailableIf('friends', err, () => listFriends())
  }
}

export async function rejectRequest(relationshipId: number): Promise<void> {
  try {
    await requestJSON<null>(fetcher(), `/api/friends/requests/${relationshipId}/reject`, { method: 'POST' })
  } catch (err) {
    throw await toCapabilityUnavailableIf('friends', err, () => listFriends())
  }
}

export async function removeFriend(relationshipId: number): Promise<void> {
  try {
    await requestJSON<null>(fetcher(), `/api/friends/${relationshipId}`, { method: 'DELETE' })
  } catch (err) {
    throw await toCapabilityUnavailableIf('friends', err, () => listFriends())
  }
}

/**
 * 拉黑。
 *
 * 服务端会**覆盖**既有关系（pending/accepted 都原地转 blocked，#29 的 `BlockUser`），
 * 所以拉黑一个好友 = 这段关系结束 + 双方列表互不可见，不需要再调一次删除。
 */
export async function blockUser(userId: number): Promise<void> {
  try {
    await requestJSON<ServerFriendship>(fetcher(), '/api/blocks', { method: 'POST', body: { userId } })
  } catch (err) {
    throw toCapabilityUnavailable('friends', err)
  }
}

/** 黑名单列表：服务端没有这个接口（见文件头），显式说到底缺什么。 */
export async function listBlocked(): Promise<BlockedEntry[]> {
  throw new FeatureUnavailable('friends', '黑名单列表')
}

/** 解除拉黑：服务端没有这个接口（见文件头）。拉黑因此是**单向不可逆**的。 */
export async function unblock(_userId: number): Promise<void> {
  throw new FeatureUnavailable('friends', '解除拉黑')
}
