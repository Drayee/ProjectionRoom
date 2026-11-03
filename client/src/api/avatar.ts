/**
 * 头像上传能力探针（票 #74 / 决议 #54 §9 的最后一条）。
 *
 * ## 为什么需要探针
 *
 * 对象存储是**可选能力**：没配 `PR_STORAGE_*` 时服务端**不注册** `/api/uploads`
 * （请求落 404），与账号层"DSN 为空即不注册 `/api/auth/*`"是同一条约定。
 * 设置页因此要能回答"这个部署有没有头像上传"—— 答错两个方向都难看：
 *
 *   - 说"有"，用户点了上传才发现 404：看起来像整站坏了；
 *   - 说"没有"，其实有：功能白丢。
 *
 * ## 探针为什么是 `POST /api/uploads` 空请求
 *
 * 那条路由只有 POST。空请求在**已注册**时于解析 multipart 之前就返回 400
 * （「请求必须是 multipart/form-data」）—— 也就是说它不会碰存储、不会写任何东西，
 * 代价是一次几百字节的往返。未注册时是 NoRoute 的纯文本 404。
 *
 * 判据因此是**状态码**，不是响应体：
 *   - `404` → 未启用（`false`）；
 *   - 其它（400 / 401 / 5xx）→ 路由在，能力可用（`true`）。
 *     401 也算"可用"：游客本来就不该上传，而"要登录"与"没这功能"必须分开说。
 *
 * ## 失败一律当作"不可用"，但不报错
 *
 * 网络不可达时返回 `false`：调用方据此显示中性的能力说明（`CapabilityNotice` 口径），
 * 而不是一条红色错误 —— 用户没做错任何事，他连按钮都还没点。
 */
import { useAuthStore } from '../stores/auth'
import { HttpError, toHttpError } from './http'

/** 这个部署是否提供头像上传。永不抛错。 */
export async function probeAvatarUpload(): Promise<boolean> {
  const auth = useAuthStore()
  try {
    const resp = await auth.authedFetch('/api/uploads', { method: 'POST' })
    return resp.status !== 404
  } catch (err) {
    // authedFetch 在"401 且刷新也失败"时抛 AuthApiError；那也说明路由存在。
    const http = toHttpError(err)
    if (http instanceof HttpError && http.status !== 0) return http.status !== 404
    return false
  }
}
