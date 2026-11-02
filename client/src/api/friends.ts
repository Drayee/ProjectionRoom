/**
 * 好友域门面（唯一被页面 import 的出口）。
 *
 * ⚠ **票 #59 之后只剩四个方法**（好友请求流水线整体退役）：
 *
 *   searchUsers(query)   → SearchHit[]
 *   blockUser(userId)    → void
 *   listBlocked()        → BlockedEntry[]
 *   unblock(userId)      → void
 *
 * 「好友列表」不在这里 —— 好友 ≡ 互关（GLOSSARY），那个列表是
 * `api/follows.ts` 的 `listFollowList('mutual')`。曾经这里也有一个
 * `listFriends()`，它读的是 friendships 表的 accepted 行；#57 把私聊门闩换成互关之后，
 * 那份数据与真实的好友集合已经不是一回事了，留着只会让页面显示一个不存在的名单。
 *
 * 「写操作返回 void」是刻意的：服务端 create/accept 的响应**不带 displayName**，
 * 硬拼一个空名字会让行显示成「未知用户」。所以写完由页面重新拉列表 ——
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
  searchUsers(query: string): Promise<import('../types/friends').SearchHit[]>
  blockUser(userId: number): Promise<void>
  listBlocked(): Promise<import('../types/friends').BlockedEntry[]>
  unblock(userId: number): Promise<void>
}

const impl: FriendsApi = isMock('friends') ? mock : real

export const searchUsers = impl.searchUsers
export const blockUser = impl.blockUser
export const listBlocked = impl.listBlocked
export const unblock = impl.unblock

/** 类型再导出：页面从门面拿类型，不必知道 `types/friends.ts` 的位置。 */
export type { BlockedEntry, PersonRef, PresenceState, SearchHit } from '../types/friends'
