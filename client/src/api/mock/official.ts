/**
 * 官方 / 推荐放映区 · 本地 mock（wayfinder #34）。
 *
 * 页面不直接 import 本文件：统一走门面 `api/official.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_OFFICIAL` 决定用这一份还是真实实现。
 *
 * 数据刻意覆盖四种要在界面上看见的形状（验收第 1 条）：
 *   1. 正常直播中的官方房；
 *   2. **主播离线中**（宽限期内的房间仍可进，不能显示成"已结束"）；
 *   3. 有密码（只给标记，不给密码）；
 *   4. 没起名字（`title` 空串 → 界面显示「未命名房间」）。
 *
 * 这里没有"空列表"的开关：mock 的职责是让页面在**有数据**时能开发，
 * 空态由真实实现（运营还没标记任何房间）验证 —— 那才是空态真正的来源。
 */
import { PUBLIC_ROOMS_PAGE_SIZE, type PublicRoom, type PublicRoomPage } from '../publicRooms'

/** 运营位次的呈现顺序：mock 直接按"已经排好序"给出（真实实现由 official_rank 决定）。 */
const OFFICIAL_ROOMS: PublicRoom[] = [
  {
    roomId: 'MOON01',
    title: '月夜放映：银翼杀手 2049',
    ownerName: '放映员',
    memberCount: 12,
    hostOnline: true,
    hostOffline: false,
    hasPassword: false,
    isOfficial: true,
  },
  {
    roomId: 'NIGHT3',
    title: '午夜故障片单',
    ownerName: '夜猫子',
    memberCount: 5,
    hostOnline: false,
    hostOffline: true,
    hasPassword: false,
    isOfficial: true,
  },
  {
    roomId: 'PRIME7',
    title: '管理员推荐：新世纪福音战士',
    ownerName: '七号机',
    memberCount: 31,
    hostOnline: true,
    hostOffline: false,
    hasPassword: true,
    isOfficial: true,
  },
  {
    roomId: 'CATS99',
    title: '',
    ownerName: '小满',
    memberCount: 2,
    hostOnline: true,
    hostOffline: false,
    hasPassword: false,
    isOfficial: true,
  },
]

/** `GET /api/public-rooms?official=true` 的 mock：同一套分页语义。 */
export async function listOfficialRooms(
  options: { limit?: number; offset?: number } = {},
): Promise<PublicRoomPage> {
  const limit = Math.max(1, options.limit ?? PUBLIC_ROOMS_PAGE_SIZE)
  const offset = Math.max(0, options.offset ?? 0)
  const items = OFFICIAL_ROOMS.slice(offset, offset + limit)
  return {
    items,
    total: OFFICIAL_ROOMS.length,
    limit,
    offset,
    official: true,
  }
}
