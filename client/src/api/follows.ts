/**
 * 关注域门面（唯一被页面 import 的出口）。wayfinder #56。
 *
 * 形状：
 *   getRelation(userId)   → FollowRelation（我与 TA 的关系 + TA 的两个计数）
 *   follow(userId)        → FollowRelation（关注后的事实）
 *   unfollow(userId)      → void（取关）
 *
 * 三条设计决定：
 *
 *  1. **写操作回传权威关系**，而不是 void。
 *     关系态是**派生**的（互关 = 两个方向都有边），前端自己记的那份迟早与服务端漂移；
 *     而"按钮显示已关注但服务端没有这条边"正是要避免的症状。
 *     所以关注成功后直接用服务端回的关系替换本地状态，不必再打一次 GET。
 *  2. **取关回 void**：取关没有"新事实"要说（就是"这条边没了"），
 *     页面把本地状态改回未关注即可（服务端也确认了它就是 no-op）。
 *  3. **不导出"关系动作"的判定函数**：那属于**页面/展示层**的决策
 *     （按钮显示"关注"还是"回关"还是"已关注"），
 *     而本文件只负责取数与写。混进来会让"数据"与"界面决策"长在一起。
 *
 * 实现由 `api/mode.ts` 的 `VITE_MOCK_FOLLOWS` 在**模块加载时**二选一：默认 mock，
 * 配成 `0`/`false`/`off`/`no` 时用真实实现（`api/real/follows.ts`）。
 */
import { isMock } from './mode'
import * as mock from './mock/follows'
import * as real from './real/follows'

/**
 * 两份实现形状必须一致 —— 类型标注就是这条契约的编译期检查点。
 * 任何一边签名漂移，这里立刻报错，而不是等到页面在运行期炸。
 */
type FollowsApi = {
  getRelation(userId: number): Promise<import('../types/follows').FollowRelation>
  follow(userId: number): Promise<import('../types/follows').FollowRelation>
  unfollow(userId: number): Promise<void>
  listFollowList(tab: import('../types/follows').FollowListTab): Promise<import('../types/follows').FollowListEntry[]>
}

const impl: FollowsApi = isMock('follows') ? mock : real

export const getRelation = impl.getRelation
export const follow = impl.follow
export const unfollow = impl.unfollow
export const listFollowList = impl.listFollowList

/** 类型再导出：页面从门面拿类型，不必知道 `types/follows.ts` 的位置。 */
export type { FollowAction, FollowListEntry, FollowListTab, FollowRelation } from '../types/follows'
