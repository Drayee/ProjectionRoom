/**
 * 「我的放映室」· 本地 mock（票 #70）。
 *
 * 页面不直接 import 本文件：统一走门面 `api/myRooms.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_ROOMS` 决定用这一份还是真实实现。
 *
 * # 数据刻意覆盖四种要在界面上看见的形状
 *
 *   1. 公开且仍在用的房（正常态）；
 *   2. **私密房**（`isPublic: false`）—— 它只应该在这一页出现，
 *      `/api/users/:id/rooms` 与公开房列表里都不该有它；
 *   3. **已关闭的房**（`isClosed: true`）—— 记录还在，房主可以重新开放；
 *   4. 有密码的房（只给标记）。
 *
 * 少任何一种，"我的放映室"这块界面就只被验证了正常态，而这一页的全部意义
 * 恰恰在"把看不见的房也列出来"。
 */
import type { MyRoomItem, MyRoomList } from '../../types/rooms'

/** mock 态里"我"就是 id 1 / 月夜猫（与其余 mock 域一致）。 */
const CURRENT_USER_ID = 1

/**
 * 我开过的房。
 *
 * 房间码与 `api/mock/official.ts` 的运营位**刻意不重叠**：那一份是"别人开的官方房"，
 * 混进来会让"我的放映室怎么出现了别人的房"变成一个要读两个文件才能回答的问题。
 */
const MY_ROOMS: MyRoomItem[] = [
  {
    roomId: 'MYROOM01',
    title: '我的常驻：周五夜场',
    isPublic: true,
    isClosed: false,
    hasPassword: false,
    createdAt: '2024-05-01T12:00:00Z',
    lastSeenAt: '2024-06-02T12:00:00Z',
  },
  {
    roomId: 'MYROOM02',
    title: '私密试片间',
    isPublic: false,
    isClosed: false,
    hasPassword: true,
    createdAt: '2024-05-10T12:00:00Z',
    lastSeenAt: '2024-06-01T12:00:00Z',
  },
  {
    roomId: 'MYROOM03',
    title: '',
    isPublic: true,
    isClosed: true,
    hasPassword: false,
    createdAt: '2024-04-01T12:00:00Z',
    lastSeenAt: '2024-04-20T12:00:00Z',
  },
]

/** 与其余 mock 域一致：分页语义照服务端，但数据量小，第二页直接空。 */
export async function listMyRooms(
  options: { page?: number; pageSize?: number } = {},
): Promise<MyRoomList> {
  const pageSize = options.pageSize && options.pageSize > 0 ? options.pageSize : 20
  const page = options.page && options.page > 0 ? options.page : 1
  const start = (page - 1) * pageSize
  const slice = MY_ROOMS.slice(start, start + pageSize)
  return {
    items: slice,
    hasMore: start + slice.length < MY_ROOMS.length,
  }
}

/** 仅供同域内其它 mock 引用（当前没有消费者，留着是为了不让人误以为要自己拼）。 */
export const MOCK_MY_ROOM_OWNER_ID = CURRENT_USER_ID
