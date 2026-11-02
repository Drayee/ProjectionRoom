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

/**
 * mock 态的放映室标题查表（收藏列表要用它给每一行配标题）。
 *
 * 找不到返回 `null`（→ 失效态）：与帖子那边同一口径。
 * 注意 mock 的官方房**没有关闭/未公开的概念**，所以这里只能表达"存在/不存在"；
 * 真实的"房间关了但条目还在"由服务端 `rooms_meta` 的 closed_at 判（见 store/favorites.go）。
 */
export function mockRoomTitle(roomId: string): string | null {
  const room = OFFICIAL_ROOMS.find((r) => r.roomId === roomId)
  if (!room) return null
  return room.title === '' ? '未命名房间' : room.title
}

/**
 * 某位房主的公开放映室（票 #68 的档案页内容块用）。
 *
 * 按 `ownerName` 匹配 —— mock 的 `OFFICIAL_ROOMS` 只有昵称没有 owner id，
 * 而档案页拿到的恰恰是昵称（`PublicProfile.displayName`）。真实实现按
 * `rooms_meta.owner_user_id` 精确匹配，两者在这里的差异只影响 mock 态，
 * 而且"重名房主会互相看到对方的房"在 8 人的假数据里不会发生。
 *
 * 刻意**不**复用 `listOfficialRooms`：那个带分页与 `official` 回显，
 * 档案页要的是"这个人的全部公开房、按最近活跃倒序"，两套语义混一起会让
 * "为什么这里少了一间房"变成一个要读两个文件才能回答的问题。
 */
export function mockRoomsByOwner(ownerName: string): Array<{
  roomId: string
  title: string
  createdAt: string
  lastSeenAt: string
}> {
  if (!ownerName) return []
  return OFFICIAL_ROOMS.filter((r) => r.ownerName === ownerName).map((r) => ({
    roomId: r.roomId,
    title: r.title,
    // mock 没有真实时间线：用固定值让"最近活跃"的展示有个确定的形状，
    // 而不是每次刷新都变（那会让真机脚本的判据不稳定）。
    createdAt: '2024-06-01T12:00:00Z',
    lastSeenAt: '2024-06-01T12:00:00Z',
  }))
}
