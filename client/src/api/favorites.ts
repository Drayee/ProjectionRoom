/**
 * 收藏域门面（唯一被页面 import 的出口）。决议 #48 / 票 #64。
 *
 * 形状：
 *   setFavorite(type, targetId, on) → FavoriteState（PUT/DELETE 的幂等 set 语义）
 *   listFavorites({type, before?, limit?}) → {items, hasMore}
 *
 * ## 为什么是 `setFavorite(…, on)` 而不是 `toggleFavorite()`
 *
 * 点赞是 toggle（POST 一个动作，服务端翻转），收藏是 **set**（PUT/DELETE 幂等）。
 * 两者的差别不是风格：
 *
 *   - toggle 在"我以为我没收藏、其实收藏了"时会**翻错方向**（用户看到的是"点了一下反而取消"）；
 *   - set 在同样的分叉下是**幂等**的（重试、双击、网络重放都不会让状态漂走）。
 *
 * 前端乐观更新时手里已经知道"目标状态应该是 true"，所以 set 正好对上它的语义。
 *
 * ## 为什么不导出任何"计数"
 *
 * 收藏没有公开计数（决议 #48 §3）。这里刻意连"某对象被收藏了多少次"的入口都不给 ——
 * 多一个取数入口，就有人会把它渲染成社交信号。
 *
 * 实现由 `api/mode.ts` 的 `VITE_MOCK_FAVORITES` 在**模块加载时**二选一：默认 mock，
 * 配成 `0`/`false`/`off`/`no` 时用真实实现（`api/real/favorites.ts`）。
 */
import { isMock } from './mode'
import * as mock from './mock/favorites'
import * as real from './real/favorites'

/**
 * 两份实现形状必须一致 —— 类型标注就是这条契约的编译期检查点。
 * 任何一边签名漂移，这里立刻报错，而不是等到页面在运行期炸。
 */
type FavoritesApi = {
  setFavorite(
    type: import('../types/favorites').FavoriteType,
    targetId: string,
    on: boolean,
  ): Promise<import('../types/favorites').FavoriteState>
  listFavorites(input?: {
    type: import('../types/favorites').FavoriteType
    before?: string
    limit?: number
  }): Promise<import('../types/favorites').FavoriteListResult>
}

const impl: FavoritesApi = isMock('favorites') ? mock : real

export const setFavorite = impl.setFavorite
export const listFavorites = impl.listFavorites

/** 类型再导出：页面从门面拿类型，不必知道 `types/favorites.ts` 的位置。 */
export type { FavoriteEntry, FavoriteListResult, FavoriteState, FavoriteType } from '../types/favorites'
