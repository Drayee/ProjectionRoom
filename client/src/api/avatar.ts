/**
 * 头像上传（票 #74/#75 / 决议 #54 §8 的「零新端点」）。
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
import { HttpError, requestJSON, toHttpError } from './http'
import type { MyProfile } from '../types/users'

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

/**
 * 把裁好的 blob 传到 `POST /api/uploads`，拿回公开 URL（票 #75）。
 *
 * ## 为什么字段名必须是 `file`
 *
 * 服务端 `nextFilePart` 只认 `part.FormName() == "file"`，其余段会被跳过并关闭。
 * 字段名写错的表现是 400「缺少 file 字段」—— 而请求体本身完全是好的，
 * 排查时会误以为是流式读取的问题。
 *
 * ## 为什么不用 `requestJSON`
 *
 * 那一层默认 `Content-Type: application/json` 并 `JSON.stringify` 请求体；
 * 这里要的是 `multipart/form-data`，且**不能**手动设 Content-Type ——
 * 浏览器必须自己补 `boundary`，手写会漏掉它，服务端 `MultipartReader()` 直接失败。
 *
 * ## 失败文案
 *
 * 服务端四支错误（`upload_bad_type` 415 / `upload_too_large` 413 /
 * `upload_unavailable` 503 / `upload_failed` 500）的 message 都是给人看的最终表述，
 * 原样透出。只有 404 例外：那是"这条路由没注册"（NoRoute 纯文本），
 * 得翻译成"这个部署没有启用文件存储"，否则用户看到的是「请求失败（HTTP 404）」。
 */
export async function uploadAvatarBlob(blob: Blob): Promise<string> {
  const auth = useAuthStore()
  const form = new FormData()
  // 文件名只是给 multipart 一个名分；服务端按 key 规则自己生成对象名，
  // 扩展名则取自 Content-Type（`PutImage` 里的 `allowedImageTypes`）。
  form.append('file', blob, blob.type === 'image/webp' ? 'avatar.webp' : 'avatar.jpg')

  let resp: Response
  try {
    resp = await auth.authedFetch('/api/uploads', { method: 'POST', body: form })
  } catch (err) {
    const http = toHttpError(err)
    if (http.status === 404) throw new HttpError({ status: 404, code: 'UPLOAD_UNAVAILABLE', message: UPLOAD_DISABLED_MESSAGE })
    throw http
  }

  const text = await resp.text().catch(() => '')
  if (!resp.ok) {
    if (resp.status === 404) {
      throw new HttpError({ status: 404, code: 'UPLOAD_UNAVAILABLE', message: UPLOAD_DISABLED_MESSAGE })
    }
    // 复用 error 解析：`parseErrorBody` 对扁平 `{error, code}` 与嵌套两种形状都认。
    throw parseUploadError(resp.status, text)
  }

  let url = ''
  try {
    const body = JSON.parse(text) as { url?: unknown }
    if (typeof body.url === 'string') url = body.url.trim()
  } catch {
    url = ''
  }
  if (url === '') {
    throw new HttpError({
      status: resp.status,
      code: 'HTTP_BAD_RESPONSE',
      message: '上传成功但服务端没有返回地址，请稍后重试',
    })
  }
  return url
}

const UPLOAD_DISABLED_MESSAGE = '这个部署没有启用文件存储，暂时不能上传头像'

/** 把非 2xx 的上传响应转成 HttpError；扁平错误体由 `parseErrorBody` 的风格解析。 */
function parseUploadError(status: number, text: string): HttpError {
  let code = `HTTP_${status}`
  let message = ''
  try {
    const body = JSON.parse(text) as { error?: unknown; code?: unknown }
    if (typeof body.code === 'string' && body.code !== '') code = body.code
    if (typeof body.error === 'string') message = body.error
  } catch {
    message = ''
  }
  if (message.trim() === '') {
    // 413 / 415 这两支即便没有文案也该说人话：它们是最常见的两种失败。
    if (status === 413) message = '图片太大，请换一张更小的'
    else if (status === 415) message = '只支持 JPEG / PNG / GIF / WebP 图片'
    else message = `上传失败（HTTP ${status}）`
  }
  return new HttpError({ status, code, message })
}

/**
 * 把头像地址落到自己的账号上（`PATCH /api/me`，票 #74 的三态之一）。
 *
 * `null` = 清除，回落到字母头。传 URL 时服务端会校验它是**本桶的合法对象地址**
 * （`storage.OwnsURL`），外链一律 400 —— 所以这里不做前端校验，做了也只是重复。
 *
 * 返回权威档案：页面直接交给 `stores/auth.ts` 的落地入口，不必再打 `/me`。
 */
export async function applyAvatarUrl(avatarUrl: string | null): Promise<MyProfile> {
  const auth = useAuthStore()
  const body = await requestJSON<{ user: ServerMeProfile }>(auth.authedFetch, '/api/me', {
    method: 'PATCH',
    body: { avatarUrl },
  })
  const raw = body?.user
  if (!raw) {
    throw new HttpError({
      status: 500,
      code: 'HTTP_BAD_RESPONSE',
      message: '服务端没有返回更新后的档案',
    })
  }
  const profile: MyProfile = {
    id: raw.id,
    username: raw.username,
    displayName: raw.displayName,
    email: raw.email ?? '',
    emailVerified: raw.emailVerified === true,
    role: raw.role,
    status: raw.status,
    createdAt: raw.createdAt,
  }
  // 与 `real/users.ts` 同一条纪律：有条目才赋，不显式写 `undefined`。
  if (typeof raw.avatarUrl === 'string' && raw.avatarUrl !== '') profile.avatarUrl = raw.avatarUrl
  return profile
}

/** `PATCH /api/me` 响应里的 user（`service.Profile`，见 `real/users.ts` 的说明）。 */
interface ServerMeProfile {
  id: number
  username: string
  displayName: string
  email?: string
  emailVerified: boolean
  role: string
  status: string
  avatarUrl?: string
  createdAt: string
}
