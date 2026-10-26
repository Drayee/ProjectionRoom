/**
 * 官方 / 推荐放映区 · 真实 HTTP 实现（wayfinder #34；服务端由 #35 冻结）。
 *
 * 它复用公开房端点：`GET /api/public-rooms?official=true`
 *   - 只列 `rooms_meta.is_official` **且**当前仍在内存里的房间；
 *   - 按 `official_rank` 降序（同 rank 按房间码），与普通列表的"实时性排序"不同；
 *   - 免登录（游客也能看运营位）。
 *
 * 两条判据都在这一层把住：
 *
 *  1. **整组路由没注册**（老实例 / 账号能力关闭）→ 404 → `CapabilityUnavailable`，
 *     页面整块换「官方分区后端尚未上线」空态。列表端点自己就是域探针，不需要另外打一发。
 *  2. **服务端还不认识 `official` 参数**（#35 上线之前的版本）→ 它会当没传，回一份
 *     **普通**公开房列表且响应里没有 `official` 回显。这时**绝不能**把这份列表当官方房
 *     渲染 —— 那等于把普通房标成运营推荐。`page.official !== true` 就判"官方区未上线"。
 *
 * 为什么要读回显而不是"看第一条是不是官方"：空官方区（合法状态）会让我们把老服务端
 * 误判成"支持但没数据"；而回显是服务端自己说"我理解了这个筛选"。
 */
import { useAuthStore } from '../../stores/auth'
import { PUBLIC_ROOMS_PAGE_SIZE, fetchPublicRooms, type PublicRoomPage } from '../publicRooms'
import { CapabilityUnavailable, toCapabilityUnavailable } from '../mode'

export async function listOfficialRooms(
  options: { limit?: number; offset?: number } = {},
): Promise<PublicRoomPage> {
  try {
    const page = await fetchPublicRooms(useAuthStore().authedFetch, {
      limit: options.limit ?? PUBLIC_ROOMS_PAGE_SIZE,
      offset: options.offset ?? 0,
      official: true,
    })
    if (!page.official) {
      // 200 但服务端没理解筛选：这是"能力未上线"，不是"官方区是空的"。
      throw new CapabilityUnavailable('official', '当前服务端还不支持官方区（/api/public-rooms?official=true）')
    }
    return page
  } catch (err) {
    throw toCapabilityUnavailable('official', err)
  }
}
