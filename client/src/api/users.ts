/**
 * 用户档案域门面（唯一被页面 import 的出口）。wayfinder #32。
 *
 * 形状：
 *   getPublicProfile(userId)  → PublicProfile   （他人视角，免登录）
 *   updateDisplayName(name)   → MyProfile       （本人视角，必须登录）
 *
 * 两条刻意的取舍：
 *
 *  1. **读端点只读公开白名单**（id / username / displayName / role / createdAt）。
 *     email / status 在 `PublicProfile` 类型里根本不存在 —— 「他人视角看不到邮箱」
 *     是类型层面的事实，而不是模板里少写一行（#33 的字段白名单用例钉住了服务端那一侧）。
 *  2. **写操作返回权威档案**：服务端清洗可能改过昵称（去零宽、清空回落用户名），
 *     所以页面把返回值直接交给 `stores/auth.ts` 的落地入口，不猜、不再打 `/me`。
 *
 * 实现由 `api/mode.ts` 的 `VITE_MOCK_USERS` 在**模块加载时**二选一：默认 mock，
 * 配成 `0`/`false`/`off`/`no` 时用真实实现（`api/real/users.ts`）。
 */
import { isMock } from './mode'
import * as mock from './mock/users'
import * as real from './real/users'

/**
 * 两份实现形状必须一致 —— 类型标注就是这条契约的编译期检查点。
 * 任何一边签名漂移，这里立刻报错，而不是等到页面在运行期炸。
 */
type UsersApi = {
  getPublicProfile(userId: number): Promise<import('../types/users').PublicProfile>
  updateDisplayName(displayName: string): Promise<import('../types/users').MyProfile>
}

const impl: UsersApi = isMock('users') ? mock : real

export const getPublicProfile = impl.getPublicProfile
export const updateDisplayName = impl.updateDisplayName

/** 类型再导出：页面从门面拿类型，不必知道 `types/users.ts` 的位置。 */
export type { MyProfile, PublicProfile } from '../types/users'
