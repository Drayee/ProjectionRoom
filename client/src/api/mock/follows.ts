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
import type { FollowListEntry, FollowListTab, FollowRelation } from '../../types/follows'

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

/**
 * 关注边集合：键是 follower + '->' + followee。
 *
 * 种子与 `api/mock/friends.ts` 的关系种子对齐（那边是旧模型：accepted/pending，
 * 本票之后「好友」只剩互关一种表达，见 #42 决议）：
 *
 *   - 1 ⇄ 2  互关（好友：放映员）
 *   - 1 ⇄ 3  互关（好友：阿梓）
 *   - 4 → 1  夜猫子关注了我，我没回关（粉丝 tab 里应显示「回关」）
 *   - 1 → 5  我关注了喵喵，TA 没回关（关注 tab 里应显示「已关注」）
 *
 * 种子里**必须**有互关的人：好友页默认落在「好友」tab，全空的话
 * `verify-avatar` 那类"好友行里有头像"的判据会没有行可查（假红）。
 */
const edges = new Set<string>([
  edgeKey(1, 2),
  edgeKey(2, 1),
  edgeKey(1, 3),
  edgeKey(3, 1),
  edgeKey(4, 1),
  edgeKey(1, 5),
])

/** 边键（函数声明会提升，所以上面的初始化里可以直接用）。 */
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

/**
 * 关注域的「快照」（给别的 mock 域复用）。
 *
 * 存在的理由：好友页的搜索结果行要显示「关注 / 已关注 / 回关 / 发消息」，
 * 而那份事实的真源在**本文件**（关注边集合）。`api/mock/friends.ts` 的
 * `searchUsers` 需要同一组布尔 —— 让它在自己那边再算一遍，就等于给
 * 同一个派生造了第二个 owner（mock 态里两份会漂移，而 mock 漂移的症状
 * 是"验收过了、翻真翻车"）。
 */
export function mockFollowSnapshot(userId: number): {
  following: boolean
  followedBy: boolean
  mutual: boolean
  canDm: boolean
  blocked: boolean
} {
  const rel = relationOf(userId)
  return {
    following: rel.following,
    followedBy: rel.followedBy,
    mutual: rel.mutual,
    canDm: rel.canDm,
    blocked: isBlockedEither(CURRENT_USER_ID, userId),
  }
}

/** 取关（幂等：重复取关不报错）。 */
export async function unfollow(userId: number): Promise<void> {
  if (!Number.isInteger(userId) || userId <= 0) throw http(400, 'BAD_REQUEST', '用户 id 不合法')
  edges.delete(edgeKey(CURRENT_USER_ID, userId))
}

/**
 * 名单（票 #58）：好友（互关）/ 关注 / 粉丝。
 *
 * 与真实实现对齐的语义：
 *
 *  1. **被拉黑的人双向隐藏**（我拉黑的 + 拉黑我的都滤掉）；
 *  2. **按关注时间倒序** —— mock 没有时间，退化成按 id 倒序，
 *     但顺序**必须稳定**（同一份名单两次渲染顺序不同会让界面闪）；
 *  3. 未知 tab 抛错（不猜）。
 */
export async function listFollowList(tab: FollowListTab): Promise<FollowListEntry[]> {
  const want = (id: number): boolean => {
    const following = edges.has(edgeKey(CURRENT_USER_ID, id))
    const followedBy = edges.has(edgeKey(id, CURRENT_USER_ID))
    if (tab === 'mutual') return following && followedBy
    if (tab === 'following') return following
    if (tab === 'followers') return followedBy
    throw http(400, 'BAD_REQUEST', 'tab 必须是 mutual / following / followers')
  }

  return PEOPLE.filter((p) => p.id !== CURRENT_USER_ID)
    .filter((p) => !isBlockedEither(CURRENT_USER_ID, p.id))
    .filter((p) => want(p.id))
    .sort((a, b) => b.id - a.id)
    .map((p) => {
      const rel = relationOf(p.id)
      return {
        userId: p.id,
        username: usernameOf(p.id),
        displayName: p.displayName,
        following: rel.following,
        followedBy: rel.followedBy,
        mutual: rel.mutual,
        canDm: rel.canDm,
      }
    })
}

/**
 * 账号名。
 *
 * mock 目录里只有 displayName，而名单行**必须**带 username（两个同昵称的人
 * 靠它区分）。这里按与 `api/mock/friends.ts` 一致的 8 个账号名给一份映射 ——
 * 两边不一致的症状是"名单里显示 @mooncat、好友页显示 @miao"。
 */
const USERNAMES: Record<number, string> = {
  1: 'mooncat',
  2: 'projectionist',
  3: 'azi',
  4: 'nightowl',
  5: 'miao',
  6: 'stray_cat',
  7: 'unit07',
  8: 'xiaoman',
}

function usernameOf(id: number): string {
  return USERNAMES[id] ?? 'user' + id
}
