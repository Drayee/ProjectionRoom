/**
 * 用户档案域 · 本地 mock（wayfinder #32）。
 *
 * 页面**不直接** import 本文件：统一走门面 `api/users.ts`，由 `api/mode.ts` 的
 * `VITE_MOCK_USERS` 决定用 mock 还是真实实现。
 *
 * 三条与真实实现刻意对齐的语义（不对齐的话 mock 态验收过了、翻真还会翻车）：
 *
 *  1. **不存在的 id 回 404**（`HttpError`，`code: 'NOT_FOUND'`）——不是空对象、
 *     也不是 `null`。页面靠它渲染「用户不存在」。
 *  2. **昵称清洗与回落**与 `service.SanitizeDisplayName` 同一套规则：去掉
 *     Cc/Cf（零宽 / Bidi / 控制符）→ 去首尾空白 → 空则回落用户名 → 限长 32。
 *  3. **写操作返回权威档案**（改完的完整 `MyProfile`），与 `PATCH /api/me` 的
 *     响应形状一致：页面据此更新 store，不必再打一发 `/me`。
 *
 * 人员目录与 `api/mock/friends.ts` 的 `PEOPLE` 保持同一批 id：mock 态里
 * 好友搜索结果与公开档案指的是同一个人，否则「点进某人的主页」会看到另一个名字。
 */
import { HttpError } from '../http'
import { useAuthStore } from '../../stores/auth'
import type { MyProfile, PublicProfile } from '../../types/users'

/** 与 `service.MaxDisplayNameLen` 一致。 */
const MAX_DISPLAY_NAME = 32

interface MockPerson {
  id: number
  username: string
  displayName: string
  role: 'user' | 'admin'
  /** 加入时间（RFC3339）。 */
  createdAt: string
}

/**
 * 本地"用户目录"。
 *
 * 与好友 mock 的 id 一一对应（1 月夜猫 … 8 小满），并补上公开档案才需要的
 * `role` / `createdAt`。2 号刻意设成 admin：这样 mock 态就能看见角色徽章长什么样，
 * 不必等真实管理端账号。
 */
const PEOPLE: MockPerson[] = [
  { id: 1, username: 'mooncat', displayName: '月夜猫', role: 'user', createdAt: '2023-10-18T09:00:00Z' },
  { id: 2, username: 'projectionist', displayName: '放映员', role: 'admin', createdAt: '2023-10-06T12:30:00Z' },
  { id: 3, username: 'azi', displayName: '阿梓', role: 'user', createdAt: '2024-01-12T18:05:00Z' },
  { id: 4, username: 'nightowl', displayName: '夜猫子', role: 'user', createdAt: '2024-02-03T23:40:00Z' },
  { id: 5, username: 'miao', displayName: '喵喵', role: 'user', createdAt: '2024-02-29T08:15:00Z' },
  { id: 6, username: 'stray_cat', displayName: '路过的猫', role: 'user', createdAt: '2024-05-20T15:20:00Z' },
  { id: 7, username: 'unit07', displayName: '七号机', role: 'user', createdAt: '2024-07-07T07:07:00Z' },
  { id: 8, username: 'xiaoman', displayName: '小满', role: 'user', createdAt: '2024-08-08T10:00:00Z' },
]

function notFound(): HttpError {
  return new HttpError({ status: 404, code: 'NOT_FOUND', message: '用户不存在' })
}

function http(status: number, code: string, message: string): HttpError {
  return new HttpError({ status, code, message })
}

/**
 * 与 `service.StripControl` 等价的前端版本：去掉 Cc（控制）与 Cf（不可见格式）。
 *
 * 用 Unicode 属性转义而不是手写码点区间：Go 侧是 `unicode.IsControl` + `unicode.In(Cf)`，
 * 手写区间会漏掉将来 Unicode 版本新增的格式符。
 */
function stripControl(value: string): string {
  return value.replace(/[\p{Cc}\p{Cf}]/gu, '')
}

/** `GET /api/users/:id` 的 mock：目录 + 当前登录者本人。 */
export async function getPublicProfile(userId: number): Promise<PublicProfile> {
  if (!Number.isInteger(userId) || userId <= 0) throw notFound()

  // 本人优先用 store 里的档案：改过昵称之后，别人（或另一个标签页）在 mock 态
  // 也应看到新名字，而不是目录里的旧值。
  const me = useAuthStore().profile
  if (me && me.id === userId) {
    return {
      id: me.id,
      username: me.username,
      displayName: me.displayName,
      role: me.role,
      createdAt: me.createdAt,
    }
  }

  const person = PEOPLE.find((p) => p.id === userId)
  if (!person) throw notFound()
  return {
    id: person.id,
    username: person.username,
    displayName: person.displayName,
    role: person.role,
    createdAt: person.createdAt,
  }
}

/** `PATCH /api/me` 的 mock：清洗 → 回落 → 限长 → 返回权威档案。 */
export async function updateDisplayName(displayName: string): Promise<MyProfile> {
  const store = useAuthStore()
  const me = store.profile
  if (!me) throw http(401, 'AUTH_EXPIRED', '登录状态已失效，请重新登录后再试')
  // 与后端 `UpdateMyDisplayName` 一致：被封禁的账号改昵称回 403（不是 404）。
  if (me.status !== 'active') throw http(403, 'FORBIDDEN', '当前账号不允许执行该操作')

  let cleaned = stripControl(displayName).trim()
  if (cleaned === '') cleaned = stripControl(me.username).trim()
  if (cleaned === '' || [...cleaned].length > MAX_DISPLAY_NAME) {
    throw http(400, 'DISPLAY_NAME_INVALID', `昵称不能为空且不超过 ${MAX_DISPLAY_NAME} 个字符`)
  }

  // 目录里若有一行是这个人，一起改：mock 态下"别人来看我"才看得到新名字。
  const row = PEOPLE.find((p) => p.id === me.id)
  if (row) row.displayName = cleaned

  return {
    id: me.id,
    username: me.username,
    displayName: cleaned,
    email: me.email,
    emailVerified: me.emailVerified === true,
    role: me.role,
    status: me.status,
    createdAt: me.createdAt,
  }
}
