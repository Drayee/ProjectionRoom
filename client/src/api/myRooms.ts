/**
 * 「我的放映室」门面（唯一被页面 import 的出口）。票 #70。
 *
 * 形状：
 *   listMyRooms({ page? }) → MyRoomList
 *
 * # 为什么单独一个门面，不并进 `api/rooms.ts`
 *
 * `api/rooms.ts` 是**房主元数据**的读写（`GET/PATCH /api/rooms/:id/meta`），
 * 它是"我手上这一间房"的操作；这里是"我开过哪些房"的清单。前者在
 * `stores/roomMeta.ts` 里被房间页直接消费、没有 mock 分支（房间页本身必须连
 * 真实信令才能跑），而清单页需要能在 mock 态开发 —— 两种开关需求混在一个文件里，
 * 会出现"房间页意外走了 mock"这种最难查的一类问题。
 *
 * # 为什么是独立开关 `VITE_MOCK_ROOMS`
 *
 * 它既不能挂在 `posts`（内容域）也不能挂在 `official`（运营位）下面：
 * 这两个域翻真而房间清单还是 mock 时，界面上会出现"我的帖是真的、我的房是假的"
 * 这种半截状态，而两处都长得像真的。
 */
import { isMock } from './mode'
import * as mock from './mock/myRooms'
import * as real from './real/myRooms'

/**
 * 两份实现形状必须一致 —— 类型标注就是这条契约的编译期检查点。
 */
type MyRoomsApi = {
  listMyRooms(options?: { page?: number; pageSize?: number }): Promise<import('../types/rooms').MyRoomList>
}

const impl: MyRoomsApi = isMock('rooms') ? mock : real

export const listMyRooms = impl.listMyRooms

/** 单页条数：与服务端 `myRoomsDefaultLimit`（20）一致。 */
export const MY_ROOMS_PAGE_SIZE = 20

/** 类型再导出：页面从门面拿类型，不必知道 `types/rooms.ts` 的位置。 */
export type { MyRoomItem, MyRoomList } from '../types/rooms'
