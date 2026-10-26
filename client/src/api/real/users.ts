/**
 * 用户档案域 · 真实 HTTP 实现（wayfinder #32；服务端契约由 #33 冻结）。
 *
 * 两个端点：
 *
 *   GET   /api/users/:id   → 200 PublicProfile（**免登录**）
 *   PATCH /api/me          → 200 `{user: Profile}`（RequireAuth；改昵称）
 *
 * 三条与其它域一致的写法：一律走 `requestJSON(authedFetch, …)`（401 自动刷新一次
 * 只在 `stores/auth.ts` 一处实现）、服务端文案原样透出、显示层派生（头像）留在前端。
 *
 * ⚠ **本域没有"整域探针"可打，因此 404 的判据与其它域不同**（重要，别照抄坏样板）：
 *
 *   posts / friends / messages 都有一个"列表"读端点可以当探针（#26/#28 的
 *   `toCapabilityUnavailableIf`）。用户域的读端点**就是资源本身** —— 拿它当探针等于
 *   拿同一个 404 自证，什么也分不出来。而 `/api/users/search` 要登录，匿名访问会 401，
 *   把它当探针会把"某人不存在"误报成"需要登录"。
 *
 *   所以这里用**响应形状**判定，这条判据是精确的：
 *
 *   | 场景 | 服务端响应 | requestJSON 归成 |
 *   | :--- | :--- | :--- |
 *   | 整域路由没注册（账号能力关闭） | NoRoute 回纯文本 `未找到该资源` | `HttpError 404 / HTTP_404` |
 *   | 用户不存在 / 被封禁（#33 统一口径） | JSON `{"error":"用户不存在","code":"NOT_FOUND"}` | `HttpError 404 / NOT_FOUND` |
 *
 *   于是"404 且 code 是本层自造的 `HTTP_404`" = 这一组路由没上线 → `CapabilityUnavailable`
 *   （页面整块换「该分区后端尚未上线」空态）；"404 且 code 是服务端的 `NOT_FOUND`" = 这个人
 *   不存在 → 原样抛出，页面渲染「用户不存在或不可见」。
 *
 *   为什么不让 `CapabilityUnavailable` 也覆盖 `NOT_FOUND`：那会把"这个人没了"说成
 *   "分区还没上线"，而后者暗示"以后再来看" —— 用户会一直等一个不会来的功能。
 */
import { HttpError, requestJSON } from '../http'
import { CapabilityUnavailable } from '../mode'
import { useAuthStore } from '../../stores/auth'
import type { AuthedFetch } from '../http'
import type { MyProfile, PublicProfile } from '../../types/users'

/** 取认证 fetch（延迟到调用时：模块加载期 Pinia 可能还没装配）。 */
function fetcher(): AuthedFetch {
  return useAuthStore().authedFetch
}

/** 服务端 `GET /api/users/:id` 的响应（#33 的 `service.PublicProfile`）。 */
interface ServerPublicProfile {
  id: number
  username: string
  displayName: string
  role: string
  createdAt: string
}

/** 服务端 `PATCH /api/me` 的响应体；`user` 是 `service.Profile`。 */
interface ServerMeResponse {
  user: ServerProfile
}

interface ServerProfile {
  id: number
  username: string
  displayName: string
  /** `*string` + omitempty：未绑定邮箱时这个键**不存在**。 */
  email?: string
  emailVerified: boolean
  role: string
  status: string
  createdAt: string
}

/**
 * 路由没注册的 404（NoRoute 纯文本）→ 能力未上线；其余错误原样上抛。
 *
 * 判据只看"404 且错误码是本地兜底的 `HTTP_404`"：那条码只会在响应体不是 JSON 时
 * 产生（见 `http.ts` 的 `parseErrorBody`），正是 NoRoute 的形状。
 */
function toUsersError(err: unknown): Error {
  if (err instanceof HttpError && err.status === 404 && err.code === 'HTTP_404') {
    return new CapabilityUnavailable('users')
  }
  return err instanceof Error ? err : new Error(String(err))
}

/** 读某个用户的公开档案。免登录：`authedFetch` 在匿名时只是不带 Authorization 头。 */
export async function getPublicProfile(userId: number): Promise<PublicProfile> {
  try {
    const body = await requestJSON<ServerPublicProfile>(fetcher(), `/api/users/${userId}`)
    if (!body) {
      // 200 但没有可解析内容：服务端契约坏了，不是"用户不存在"。
      throw new HttpError({
        status: 500,
        code: 'HTTP_BAD_RESPONSE',
        message: '服务端返回了空的用户档案',
      })
    }
    return {
      id: body.id,
      username: body.username,
      displayName: body.displayName,
      role: body.role,
      createdAt: body.createdAt,
    }
  } catch (err) {
    throw toUsersError(err)
  }
}

/**
 * 改自己的昵称。
 *
 * 响应回的是**清洗之后的权威档案**（去零宽、回落用户名都可能改过值），所以直接
 * 用它落地 store，不必再打一发 `/me`。403 在这里有明确含义（账号被封禁），
 * 页面会把它与 404 分开说明。
 */
export async function updateDisplayName(displayName: string): Promise<MyProfile> {
  try {
    const body = await requestJSON<ServerMeResponse>(fetcher(), '/api/me', {
      method: 'PATCH',
      body: { displayName },
    })
    const raw = body?.user
    if (!raw) {
      throw new HttpError({
        status: 500,
        code: 'HTTP_BAD_RESPONSE',
        message: '服务端没有返回更新后的档案',
      })
    }
    return {
      id: raw.id,
      username: raw.username,
      displayName: raw.displayName,
      email: raw.email ?? '',
      emailVerified: raw.emailVerified === true,
      role: raw.role,
      status: raw.status,
      createdAt: raw.createdAt,
    }
  } catch (err) {
    throw toUsersError(err)
  }
}
