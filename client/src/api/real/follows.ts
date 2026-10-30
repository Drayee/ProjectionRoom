/**
 * 关注域 · 真实 HTTP 实现（wayfinder #56）。
 *
 * 三个端点（全部要登录）：
 *
 *   GET    /api/follows/:userId   → 200 FollowRelation（我与 TA 的关系）
 *   POST   /api/follows           {userId} → 201 FollowRelation（关注）
 *   DELETE /api/follows/:userId   → 204（取关）
 *
 * 三条与其它域一致的写法：一律走 requestJSON(authedFetch, …)
 * （401 自动刷新一次的语义只在 stores/auth.ts 一处实现），
 * 服务端文案原样透出、显示层派生（头像）留在前端。
 *
 * ⚠ 404 的两种含义必须分开：
 *
 *   - 「这组路由没注册」= 能力未上线 → CapabilityUnavailable；
 *   - 「这个人不存在 / 被封禁」= 服务端的 NOT_FOUND（原样上抛）。
 *
 * 两者都是 404，本层用**错误码**区分：服务端自己的 NOT_FOUND 带 code，
 * 而路由没注册时 NoRoute 回的是纯文本 → 本层兜底码 HTTP_404。
 * 这与 api/real/users.ts 的判据完全同形（那里也踩过这个坑）。
 */
import { HttpError, requestJSON } from '../http'
import { CapabilityUnavailable } from '../mode'
import { useAuthStore } from '../../stores/auth'
import type { AuthedFetch } from '../http'
import type { FollowRelation } from '../../types/follows'

/** 取认证 fetch（延迟到调用时：模块加载期 Pinia 可能还没装配）。 */
function fetcher(): AuthedFetch {
  return useAuthStore().authedFetch
}

/** 服务端 followView（#56 冻结）。 */
interface ServerFollowRelation {
  userId: number
  displayName: string
  following: boolean
  followedBy: boolean
  mutual: boolean
  canDm: boolean
  followerCount: number
  followingCount: number
}

/**
 * 路由没注册的 404（NoRoute 纯文本）→ 能力未上线；其余错误原样上抛。
 *
 * 判据只看「404 且错误码是本地兜底的 HTTP_404」：那条码只会在响应体不是 JSON 时
 * 产生（见 http.ts 的 parseErrorBody），正是 NoRoute 的形状。
 */
function toFollowsError(err: unknown): Error {
  if (err instanceof HttpError && err.status === 404 && err.code === 'HTTP_404') {
    return new CapabilityUnavailable('follows')
  }
  return err instanceof Error ? err : new Error(String(err))
}

/** 服务端响应 → 前端形状（服务端契约里没有显示层字段，这里也不加）。 */
function toRelation(raw: ServerFollowRelation): FollowRelation {
  return {
    userId: raw.userId,
    displayName: raw.displayName || '未知用户',
    following: raw.following === true,
    followedBy: raw.followedBy === true,
    mutual: raw.mutual === true,
    canDm: raw.canDm === true,
    followerCount: raw.followerCount ?? 0,
    followingCount: raw.followingCount ?? 0,
  }
}

/** 查我与某人的关系。 */
export async function getRelation(userId: number): Promise<FollowRelation> {
  try {
    const body = await requestJSON<ServerFollowRelation>(fetcher(), '/api/follows/' + userId)
    if (!body) {
      throw new HttpError({
        status: 500,
        code: 'HTTP_BAD_RESPONSE',
        message: '服务端返回了空的关系视图',
      })
    }
    return toRelation(body)
  } catch (err) {
    throw toFollowsError(err)
  }
}

/**
 * 关注某人。
 *
 * 返回服务端算好的**权威关系**：互关与 canDm 都是派生的，
 * 前端自己记的那份迟早与服务端漂移，所以这里不回传 void。
 *
 * 语义失败由服务端给：400 FOLLOW_SELF_TARGET（自关注）、
 * 403 FOLLOW_BLOCKED（拉黑存续期）、404 NOT_FOUND（不存在/被封禁）。
 * 这里**不预判、不改写文案** —— requestJSON 已经把服务端 message 原样带出来。
 */
export async function follow(userId: number): Promise<FollowRelation> {
  try {
    const body = await requestJSON<ServerFollowRelation>(fetcher(), '/api/follows', {
      method: 'POST',
      body: { userId },
    })
    if (!body) {
      throw new HttpError({
        status: 500,
        code: 'HTTP_BAD_RESPONSE',
        message: '服务端没有返回关注后的关系',
      })
    }
    return toRelation(body)
  } catch (err) {
    throw toFollowsError(err)
  }
}

/** 取关。服务端回 204（没有响应体）。 */
export async function unfollow(userId: number): Promise<void> {
  try {
    await requestJSON<null>(fetcher(), '/api/follows/' + userId, { method: 'DELETE' })
  } catch (err) {
    throw toFollowsError(err)
  }
}
