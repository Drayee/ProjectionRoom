/**
 * 关注域 · 本地 mock（wayfinder #56）。
 *
 * 页面**不直接** import 本文件 —— 统一走门面 api/follows.ts，
 * 由 api/mode.ts 的 VITE_MOCK_FOLLOWS 决定用这一份。
 *
 * 与真实实现刻意对齐的语义（不然 mock 态验收过了、翻真还会翻车）：
 *
 *  1. **关注是幂等的**：重复关注不报错，返回同一条边（服务端用唯一键 +
 *     ON CONFLICT DO NOTHING）。
 *  2. **自关注 → 400**（服务端：FOLLOW_SELF_TARGET）。
 *  3. **不存在 → 404**（NOT_FOUND）。
 *  4. **拉黑存续期 → 403**（FOLLOW_BLOCKED，方向不区分）。
 *  5. **互关是派生的**：两个方向都有边才算互关，canDm 还额外要求未被拉黑。
 *
 * 人物目录与 api/mock/friends.ts、api/mock/users.ts 保持同一批 id
 * （1 月夜猫 … 8 小满），否则 mock 态里「点进某人的主页」会看到另一个名字。
 */
import { HttpError } from '../http'
import type { FollowRelation } from '../../types/follows'

/** 当前登录用户 id：与 api/mock/friends.ts、api/mock/posts.ts 的「我」一致。 */
const CURRENT_USER_ID = 1

/** 本地「用户目录」：只有出现在这里的人才能被关注。 */
const PEOPLE: { id: number; displayName: string }[] = [
  { id: 1, displayName: '月夜猫' },
  { id: 2, displayName: '放映员' },
  { id: 3, displayName: '阿梓' },
  { id: 4, displayName: '夜猫子' },
  { id: 5, displayName: '喵喵' },
  { id: 6, displayName: '路过的猫' },
  { id: 7, displayName: '七号机' },
  { id: 8, displayName: '小满' },
]

/** 我拉黑了谁（与 api/mock/friends.ts 的种子一致：id 6 被我拉黑）。 */
const BLOCKED_BY_ME = new Set<number>([6])

/** 谁拉黑了我（拉黑存续期同样禁止关注，且方向不区分）。 */
const BLOCKED_ME = new Set<number>([7])

/** 关注边集合：键是 follower + '->' + followee。 */
const edges = new Set<string>()

function edgeKey(follower: number, followee: number): string {
  return follower + '->' + followee
}

function person(id: number) {
  return PEOPLE.find((p) => p.id === id)
}

function http(status: number, code: string, message: string): HttpError {
  return new HttpError({ status, code, message })
}

/** 拉黑判定（方向不区分，与服务端口径一致）。 */
function isBlockedEither(a: number, b: number): boolean {
  return (BLOCKED_BY_ME.has(b) && a === CURRENT_USER_ID) || (BLOCKED_ME.has(a) && b === CURRENT_USER_ID)
}

/** 构造关系视图（与服务端 followView 同形状）。 */
function relationOf(userId: number): FollowRelation {
  const p = person(userId)
  const following = edges.has(edgeKey(CURRENT_USER_ID, userId))
  const followedBy = edges.has(edgeKey(userId, CURRENT_USER_ID))
  const mutual = following && followedBy
  return {
    userId,
    displayName: p?.displayName ?? '未知用户',
    following,
    followedBy,
    mutual,
    canDm: mutual && !isBlockedEither(CURRENT_USER_ID, userId),
    followerCount: countFollowers(userId),
    followingCount: countFollowing(userId),
  }
}

/** 关注某人的人数（followerCount）。 */
function countFollowers(userId: number): number {
  let n = 0
  for (const key of edges) {
    const parts = key.split('->')
    if (Number(parts[1]) === userId) n++
  }
  return n
}

/** 某人关注的人数（followingCount）。 */
function countFollowing(userId: number): number {
  let n = 0
  for (const key of edges) {
    const parts = key.split('->')
    if (Number(parts[0]) === userId) n++
  }
  return n
}

// ---------- 导出的 API（形状必须与 api/real/follows.ts 完全一致）----------

/** 查我与某人的关系（档案页要靠它画按钮）。 */
export async function getRelation(userId: number): Promise<FollowRelation> {
  if (!Number.isInteger(userId) || userId <= 0) throw http(404, 'NOT_FOUND', '用户不存在')
  if (!person(userId)) throw http(404, 'NOT_FOUND', '用户不存在')
  return relationOf(userId)
}

/** 关注某人（幂等）。 */
export async function follow(userId: number): Promise<FollowRelation> {
  if (!Number.isInteger(userId) || userId <= 0) throw http(400, 'FOLLOW_SELF_TARGET', '不能关注自己')
  if (userId === CURRENT_USER_ID) throw http(400, 'FOLLOW_SELF_TARGET', '不能关注自己')
  if (!person(userId)) throw http(404, 'NOT_FOUND', '用户不存在')
  if (isBlockedEither(CURRENT_USER_ID, userId)) {
    throw http(403, 'FOLLOW_BLOCKED', '你们之间存在拉黑关系，无法关注')
  }
  edges.add(edgeKey(CURRENT_USER_ID, userId))
  return relationOf(userId)
}

/** 取关（幂等：重复取关不报错）。 */
export async function unfollow(userId: number): Promise<void> {
  if (!Number.isInteger(userId) || userId <= 0) throw http(400, 'BAD_REQUEST', '用户 id 不合法')
  edges.delete(edgeKey(CURRENT_USER_ID, userId))
}
