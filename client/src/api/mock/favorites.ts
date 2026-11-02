/**
 * 收藏域 · 本地 mock（决议 #48 / 票 #64）。
 *
 * 页面不直接 import 本文件：统一走门面 `api/favorites.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_FAVORITES` 决定用这一份还是真实实现。
 *
 * 状态在 `mock/favoriteStore.ts`（帖子 mock 也要读它，见那个文件的"为什么单独一个模块"）。
 * 这里只负责**把键翻译成列表行**：查标题、判失效、按时间倒序、切游标。
 *
 * ## 失效态怎么造
 *
 * 种子里的 `post:9999` 在帖子 mock 里不存在 —— 于是列表里它会渲染成
 * 「已失效」且不可点。这不是"故意留个坑"，而是决议 #48 §5 那条语义在 mock 态
 * 唯一的可观察形态：条目保留、不静默删除。没有它，失效态要等真有人删了帖子
 * 才第一次被渲染，而那时出问题已经太晚。
 */
import { HttpError } from '../http'
import type { FavoriteEntry, FavoriteListResult, FavoriteState, FavoriteType } from '../../types/favorites'
import { keysOfType, setFavorited, stampOf } from './favoriteStore'
import { mockPostTitle } from './posts'
import { mockRoomTitle } from './official'
import { MOCK_ANIMES } from './animeSeed'

/** 单页条数：与服务端 `defaultFavoriteLimit` 同值（20）。 */
const PAGE_SIZE = 20

/**
 * 标题查表：`null` = 对象已失效。
 *
 * 三种类型各查各的源 —— 这正是"多态"的代价，也是为什么真实现里三支 LEFT JOIN
 * 是分开写的（见 `internal/store/favorites.go` 的 `ListFavorites`）。
 */
function titleOf(type: FavoriteType, targetId: string): string | null {
  if (type === 'post') return mockPostTitle(Number(targetId))
  if (type === 'room') return mockRoomTitle(targetId)
  const anime = MOCK_ANIMES.find((a) => String(a.id) === targetId)
  return anime ? anime.title : null
}

/**
 * 对象是否存在（收藏前校验，对应服务端的"不存在 → 404，不写孤儿行"）。
 *
 * mock 里"存在"就是"查得到标题"。注意：**失效态与不存在是两件事** ——
 * 已收藏但对象后来没了，条目仍要留在列表里；而"现在去收藏一个不存在的东西"
 * 必须 404。两者的分界是"有没有那一行收藏记录"，不是"对象在不在"。
 */
function targetExists(type: FavoriteType, targetId: string): boolean {
  return titleOf(type, targetId) !== null
}

/** 收藏/取消收藏（幂等 set）。 */
export async function setFavorite(
  type: FavoriteType,
  targetId: string,
  on: boolean,
): Promise<FavoriteState> {
  // 只有"收藏"需要校验对象存在；取消收藏永远允许（哪怕对象已经没了，
  // 用户就是要在失效条目上点删除）。
  if (on && !targetExists(type, targetId)) {
    throw new HttpError({ status: 404, code: 'NOT_FOUND', message: '对象不存在' })
  }
  const changed = setFavorited(type, targetId, on)
  const state: FavoriteState = { targetId, targetType: type, favorited: on }
  if (on) state.created = changed
  else state.removed = changed
  return state
}

/**
 * 收藏列表（type 过滤 + 收藏时间倒序 + 游标分页）。
 *
 * 游标在这里是"上一页最后一行的 targetId"——mock 不需要真游标（数据量小），
 * 但它必须**支持** `before` 参数，否则页面那套无限滚动在 mock 态跑不通，
 * 而"分页只在真环境才验证过"正是最容易坏的那半。
 */
export async function listFavorites(
  input: { type: FavoriteType; before?: string; limit?: number } = { type: 'post' },
): Promise<FavoriteListResult> {
  const limit = Math.max(1, Math.min(input.limit ?? PAGE_SIZE, 100))
  const all = keysOfType(input.type)
    .map((targetId) => ({
      targetId,
      targetType: input.type,
      stamp: stampOf(input.type, targetId),
    }))
    // 倒序：新收藏在前。时间相同时按 targetId 降序，与服务端的
    // `Order("f.created_at DESC, f.target_id DESC")` 同口径（否则分页边界会重复/漏行）。
    .sort((a, b) => (a.stamp === b.stamp ? b.targetId.localeCompare(a.targetId) : b.stamp.localeCompare(a.stamp)))

  const start = input.before ? all.findIndex((row) => row.targetId === input.before) + 1 : 0
  const slice = all.slice(start, start + limit)

  const items: FavoriteEntry[] = slice.map((row) => {
    const title = titleOf(input.type, row.targetId)
    return {
      targetId: row.targetId,
      targetType: input.type,
      // 失效时给占位文案而不是空串：空串会让列表出现一排空白行，
      // 看起来像渲染坏了，而不是"这个对象没了"。
      title: title ?? '已失效的内容',
      valid: title !== null,
      createdAt: row.stamp,
      cursor: row.targetId,
    }
  })

  return { items, hasMore: start + limit < all.length }
}
