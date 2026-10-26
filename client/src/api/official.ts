/**
 * 官方 / 推荐放映区门面（唯一被页面 import 的出口）。wayfinder #34。
 *
 * 形状：
 *   listOfficialRooms(options?) → PublicRoomPage
 *
 * 与 `/rooms` 共用同一个数据形状（`PublicRoom`）与同一张卡片（`features/rooms/RoomCard.vue`）：
 * 官方区不是"另一种房间"，差别只有 `isOfficial` 与排序口径（运营位次 vs 实时性）。
 * 因此这里只复用 `api/publicRooms.ts` 的归一化与分页语义，不另造一套字段。
 *
 * 实现由 `api/mode.ts` 的 `VITE_MOCK_OFFICIAL` 在**模块加载时**二选一：默认 mock，
 * 配成 `0`/`false`/`off`/`no` 时用真实实现（`api/real/official.ts`）。
 */
import { isMock } from './mode'
import * as mock from './mock/official'
import * as real from './real/official'

/**
 * 两份实现形状必须一致 —— 类型标注就是这条契约的编译期检查点。
 */
type OfficialApi = {
  listOfficialRooms(options?: { limit?: number; offset?: number }): Promise<import('./publicRooms').PublicRoomPage>
}

const impl: OfficialApi = isMock('official') ? mock : real

export const listOfficialRooms = impl.listOfficialRooms

/** 官方区空态文案（与公开列表的空态分开：两处的"为什么空"不是同一个答案）。 */
export const OFFICIAL_ROOMS_EMPTY_TEXT = '官方区还没有放映'

/** 单页条数沿用公开列表的约定（20）；官方区是运营位，一屏通常远少于这个数。 */
export const OFFICIAL_ROOMS_PAGE_SIZE = 20

/** 类型再导出：页面从门面拿类型，不必知道 `api/publicRooms.ts` 的位置。 */
export type { PublicRoom, PublicRoomPage } from './publicRooms'
