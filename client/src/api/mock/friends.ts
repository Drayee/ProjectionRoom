/**
 * 好友域 · 本地 mock。
 *
 * ⚠ **票 #59 之后本文件只剩三件事**：找人（搜索）、拉黑、黑名单。
 * 「好友列表」「待处理请求」「发请求 / 接受 / 拒绝 / 删好友」整套流水线已随
 * 服务端端点一起退役 —— 好友 ≡ 互关，那个列表走 `api/mock/follows.ts` 的
 * `listFollowList('mutual')`，本文件不该再有第二份实现。
 *
 * 页面**不直接** import 本文件 —— 统一走门面 `api/friends.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_FRIENDS` 决定是否用这一份。
 *
 * 一条与真实实现刻意对齐的语义：**拉黑状态只有一份真源**。
 * `blockUser` / `listBlocked` / `unblock` 全部委托给 `api/mock/follows.ts`
 * （`mockBlock` / `mockBlockedIds` / `mockUnblock`），因为拉黑是关注域的约束
 * （存续期双方都不能新关注）。若本文件另存一份，两份会漂移，
 * 症状是"黑名单里有他、搜索结果里却能点关注"。
 *
 * 人物目录与 `api/mock/follows.ts`、`api/mock/posts.ts` 保持同一批 id
 * （1 月夜猫 … 8 小满），否则 mock 态里「点进某人的主页」会看到另一个名字。
 */
import { HttpError } from '../http'
import { avatarOf } from '../../utils/avatar'
import { mockBlock, mockBlockedIds, mockFollowSnapshot, mockUnblock } from './follows'
import type { BlockedEntry, PresenceState, SearchHit } from '../../types/friends'

/** 当前登录用户 id：与 `api/mock/posts.ts` 的 mock 当前用户一致（同一个"我"）。 */
const CURRENT_USER_ID = 1

interface MockPerson {
  id: number
  /** 登录名（搜索按它与昵称一起匹配，真实接口也是这两列）。 */
  username: string
  displayName: string
  presence: PresenceState
}

/** 本地"用户目录"：只有出现在这里的人才能被搜到。 */
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

function person(id: number): MockPerson | undefined {
  return PEOPLE.find((p) => p.id === id)
}

/** 把 mock 人物转成页面用的显示形状（头像派生与服务端适配层同一套）。 */
function refOf(p: MockPerson) {
  return { id: p.id, displayName: p.displayName, ...avatarOf(p.id, p.displayName) }
}

function conflict(status: number, code: string, message: string): HttpError {
  return new HttpError({ status, code, message })
}

// ---------- 导出的 API（形状必须与 api/real/friends.ts 完全一致）----------

/**
 * 按用户名/昵称找人。空查询返回空数组（页面在本地也会拦，这里兜一道）。
 *
 * 关系事实整组来自关注域（`mockFollowSnapshot`），本文件一个字都不算 ——
 * 那是 I3 在 mock 里的样子：关系判定只有一个 owner。
 */
export async function searchUsers(query: string): Promise<SearchHit[]> {
  const q = query.trim().toLowerCase()
  if (q === '') return []
  return PEOPLE.filter((p) => p.id !== CURRENT_USER_ID)
    .filter((p) => p.username.toLowerCase().includes(q) || p.displayName.toLowerCase().includes(q))
    .map((p) => ({ ...refOf(p), ...mockFollowSnapshot(p.id) }))
}

/**
 * 拉黑某人。
 *
 * 委托给关注域：那边会在同一步里删掉「我→TA」的关注边（保留「TA→我」），
 * 与服务端 `BlockUser` 的事务两件事一一对应。
 */
export async function blockUser(userId: number): Promise<void> {
  if (userId === CURRENT_USER_ID) throw conflict(400, 'SELF_RELATION', '不能对自己建立关系')
  if (!person(userId)) throw conflict(404, 'NOT_FOUND', '用户不存在')
  mockBlock(userId)
}

/** 黑名单：只列**我拉黑的**人（对方拉黑我的那一份，服务端也不让我看见）。 */
export async function listBlocked(): Promise<BlockedEntry[]> {
  return mockBlockedIds().map((id) => {
    const p = person(id)
    // 人物目录里查不到（真实实现里不可能，mock 的目录允许这种残行）：
    // 退化成一个可见的占位人，而不是把 undefined 塞进模板。
    return { user: p ? refOf(p) : { id, displayName: '未知用户', ...avatarOf(id, '') } }
  })
}

/** 解除拉黑：从黑名单移除（关系回到"没有关系"，不是回到好友）。 */
export async function unblock(userId: number): Promise<void> {
  if (!mockBlockedIds().includes(userId)) throw conflict(404, 'NOT_FOUND', '黑名单里没有这个人')
  mockUnblock(userId)
}
