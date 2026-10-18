/**
 * 好友域门面（唯一被页面 import 的出口）。
 *
 * 形状（#28 定稿，`/messages` 票 #30 只消费 `conversationPath`，不消费这些函数）：
 *   listFriends()                  → FriendEntry[]
 *   listRequests()                 → {incoming, outgoing}
 *   searchUsers(query)             → SearchHit[]           （real 抛 FeatureUnavailable）
 *   sendRequest(userId)            → void
 *   acceptRequest(relationshipId)  → void
 *   rejectRequest(relationshipId)  → void
 *   removeFriend(relationshipId)   → void
 *   blockUser(userId)              → void
 *   listBlocked()                  → BlockedEntry[]        （real 抛 FeatureUnavailable）
 *   unblock(userId)                → void                  （real 抛 FeatureUnavailable）
 *
 * 三个「写操作返回 void」是刻意的：服务端 create/accept 的响应**不带 displayName**，
 * 硬拼一个空名字会让"刚加的好友"显示成「未知用户」。所以写完由页面重新拉列表 ——
 * 权威数据只从读端点来，mock 与 real 因此行为一致。
 *
 * 实现由 `api/mode.ts` 的 `VITE_MOCK_FRIENDS` 在**模块加载时**二选一：默认 mock，
 * 配成 `0`/`false`/`off`/`no` 时用真实实现（`api/real/friends.ts`）。
 */
import { isMock } from './mode'
import * as mock from './mock/friends'
import * as real from './real/friends'

/**
 * 两份实现形状必须一致 —— 类型标注就是这条契约的编译期检查点。
 * 任何一边签名漂移，这里立刻报错，而不是等到页面在运行期炸。
 */
type FriendsApi = {
  listFriends(): Promise<import('../types/friends').FriendEntry[]>
  listRequests(): Promise<import('../types/friends').FriendRequests>
  searchUsers(query: string): Promise<import('../types/friends').SearchHit[]>
  sendRequest(userId: number): Promise<void>
  acceptRequest(relationshipId: number): Promise<void>
  rejectRequest(relationshipId: number): Promise<void>
  removeFriend(relationshipId: number): Promise<void>
  blockUser(userId: number): Promise<void>
  listBlocked(): Promise<import('../types/friends').BlockedEntry[]>
  unblock(userId: number): Promise<void>
}

const impl: FriendsApi = isMock('friends') ? mock : real

export const listFriends = impl.listFriends
export const listRequests = impl.listRequests
export const searchUsers = impl.searchUsers
export const sendRequest = impl.sendRequest
export const acceptRequest = impl.acceptRequest
export const rejectRequest = impl.rejectRequest
export const removeFriend = impl.removeFriend
export const blockUser = impl.blockUser
export const listBlocked = impl.listBlocked
export const unblock = impl.unblock

/** 类型再导出：页面从门面拿类型，不必知道 `types/friends.ts` 的位置。 */
export type {
  BlockedEntry,
  FriendEntry,
  FriendRequest,
  FriendRequests,
  PersonRef,
  PresenceState,
  RelationState,
  SearchHit,
} from '../types/friends'
