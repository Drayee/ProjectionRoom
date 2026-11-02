/**
 * 收藏域 · 真实 HTTP 实现（决议 #48 / 票 #64）。
 *
 * 服务端契约（`internal/handler/favorites.go` 已冻结）：
 *   PUT    /api/posts/:id/favorite     → FavoriteState（幂等 set）
 *   DELETE /api/posts/:id/favorite     → FavoriteState
 *   PUT    /api/animes/:id/favorite    → FavoriteState
 *   DELETE /api/animes/:id/favorite    → FavoriteState
 *   PUT    /api/rooms/:roomId/favorite → FavoriteState
 *   DELETE /api/rooms/:roomId/favorite → FavoriteState
 *   GET    /api/favorites?type=&before=&limit= → {items, hasMore}
 *
 * ## 三种路径的拼法为什么在一张表里
 *
 * 三条写路径的形状完全一样，只有**路径段**不同（`posts` / `animes` / `rooms`）。
 * 分别写三个函数会把"错误归一"和"字段映射"复制三份，而它们必须完全一致 ——
 * 复制三份的典型症状是"帖子收藏 404 显示成人话、放映室收藏 404 显示成裸错误"。
 *
 * ## 错误归一
 *
 * 整域 404（路由没注册）→ `CapabilityUnavailable`；对象不存在也是 404，
 * 但服务端给了 JSON `{error, code}`，所以 `toCapabilityUnavailable` 里那条
 * "只认 HTTP_404 兜底码"的判据会把两者分开 —— 与帖子域同一套（见 `api/posts.ts` 的说明）。
 */
import { requestJSON, query, type AuthedFetch } from '../http'
import { toCapabilityUnavailable } from '../mode'
import type {
  FavoriteEntry,
  FavoriteListResult,
  FavoriteState,
  FavoriteType,
} from '../../types/favorites'
import { useAuthStore } from '../../stores/auth'

/** 单页条数：与服务端 `defaultFavoriteHTTPLimit` 同值（20）。 */
const PAGE_SIZE = 20

/** 取认证 fetch（延迟到调用时：模块加载期 store 可能还没装配）。 */
function fetcher(): AuthedFetch {
  return useAuthStore().authedFetch
}

/**
 * 写路径的路径段表。
 *
 * 用一个 Record 而不是三个函数：漏一种类型时 TS 会立刻报缺键
 * （与 `api/mode.ts` 的 `SWITCHES` 同一个手法）。
 */
const PATH_SEGMENT: Record<FavoriteType, string> = {
  post: 'posts',
  anime: 'animes',
  room: 'rooms',
}

/** 服务端返回的状态行。 */
interface ServerFavoriteState {
  targetId: string
  targetType: FavoriteType
  favorited: boolean
  created?: boolean
  removed?: boolean
}

/** 服务端返回的列表行。 */
interface ServerFavoriteEntry {
  targetId: string
  targetType: FavoriteType
  title: string
  valid: boolean
  createdAt: string
  cursor: string
}

function toState(raw: ServerFavoriteState | null, type: FavoriteType, targetId: string, on: boolean): FavoriteState {
  const state: FavoriteState = {
    targetId: raw?.targetId ?? targetId,
    targetType: raw?.targetType ?? type,
    // 服务端是权威；响应缺字段时退回"我刚请求的那个状态" ——
    // 这比退回 false 好：false 会让按钮在成功的请求之后灭掉。
    favorited: raw?.favorited ?? on,
  }
  if (raw?.created) state.created = true
  if (raw?.removed) state.removed = true
  return state
}

function toEntry(raw: ServerFavoriteEntry): FavoriteEntry {
  return {
    targetId: raw.targetId,
    targetType: raw.targetType,
    title: raw.title === '' ? '已失效的内容' : raw.title,
    valid: raw.valid === true,
    createdAt: raw.createdAt,
    cursor: raw.cursor,
  }
}

/** 收藏 / 取消收藏（幂等 set：PUT 与 DELETE）。 */
export async function setFavorite(
  type: FavoriteType,
  targetId: string,
  on: boolean,
): Promise<FavoriteState> {
  const path = `/api/${PATH_SEGMENT[type]}/${encodeURIComponent(targetId)}/favorite`
  try {
    const body = await requestJSON<ServerFavoriteState>(fetcher(), path, {
      method: on ? 'PUT' : 'DELETE',
    })
    return toState(body, type, targetId, on)
  } catch (err) {
    throw toCapabilityUnavailable('favorites', err)
  }
}

/** 收藏列表（type 必填：三种对象混排没有定义好的排序）。 */
export async function listFavorites(
  input: { type: FavoriteType; before?: string; limit?: number } = { type: 'post' },
): Promise<FavoriteListResult> {
  try {
    const body = await requestJSON<{ items: ServerFavoriteEntry[]; hasMore: boolean }>(
      fetcher(),
      `/api/favorites${query({ type: input.type, before: input.before, limit: input.limit ?? PAGE_SIZE })}`,
    )
    return { items: (body?.items ?? []).map(toEntry), hasMore: body?.hasMore ?? false }
  } catch (err) {
    throw toCapabilityUnavailable('favorites', err)
  }
}
