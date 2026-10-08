import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import * as api from '../api/admin'
import type { AdminAuditRow, AdminLogPage, AdminMetrics, AdminRoom, AdminUser } from '../api/admin'
import { toHttpError, type AuthedFetch } from '../api/http'
import { useAuthStore } from './auth'

/** 管理端的五个区块。标签页顺序 = 排障时最常走的顺序（先看人、再看房间、最后看日志/审计）。 */
export type AdminSection = 'users' | 'rooms' | 'metrics' | 'logs' | 'audit'

/** 每页条数：与既有一期管理端脚本（verify-auth ⑨ 用 limit=5）不冲突，这里取一个屏幕能读完的量。 */
export const ADMIN_PAGE_SIZE = 20

/** 服务端给出的权限裁决：无权限 / 未登录 / 管理端未启用 / 其它失败。 */
export type AdminDenied = '' | 'forbidden' | 'unauthenticated' | 'unavailable'

interface Paged<T> {
  items: T[]
  total: number
  offset: number
  limit: number
  loading: boolean
  error: string
}

function emptyPage<T>(): Paged<T> {
  return { items: [], total: 0, offset: 0, limit: ADMIN_PAGE_SIZE, loading: false, error: '' }
}

/**
 * 管理端状态（`/admin`）。
 *
 * 三条刻意的取舍：
 *   1. **授权判定的唯一来源是服务端的响应**。这里不读本地缓存的 role 来决定"能不能进"
 *      —— 缓存里的 role 只决定**入口是否显示**（体验优化），真正的门是 `RequireAdmin`。
 *      因此非 admin 打开 /admin 的方式是"照常发一次请求，拿到 403 就渲染无权限"，
 *      而不是"本地一看不是 admin 就空白"。
 *   2. **401 与 403 分诊**：401 说明会话真的没了（可以给登录入口），403 说明会话好好的、
 *      只是权限不够（给"返回首页"，不给登录页 —— 那会让人以为自己的登录出了问题）。
 *      两者都**不带人跳转**（authedFetch 传 redirectOnAuthFailure:false）：把人拽走
 *      等于把这个结论抹掉。
 *   3. **每个区块自己持分页游标**：审计没有 total（服务端不做 COUNT），
 *      用户/房间/日志有 total；把两种判据统一成"本页是否满页"既简单又对两者都成立。
 */
export const useAdminStore = defineStore('admin', () => {
  const auth = useAuthStore()

  const section = ref<AdminSection>('users')
  /** 首次加载（决定整页显示骨架还是内容）。 */
  const booting = ref(true)
  /** 整页级的失败（服务端拒绝/不可用），与区块级 error 分开：它决定**整页**换成提示。 */
  const denied = ref<AdminDenied>('')
  const deniedText = ref('')

  const users = ref<Paged<AdminUser>>(emptyPage<AdminUser>())
  const rooms = ref<Paged<AdminRoom>>(emptyPage<AdminRoom>())
  const metrics = ref<{ data: AdminMetrics; loading: boolean; error: string }>({
    data: {},
    loading: false,
    error: '',
  })
  const logs = ref<Paged<string>>(emptyPage<string>())
  const audit = ref<Paged<AdminAuditRow>>(emptyPage<AdminAuditRow>())

  /** 搜索/筛选条件（搜索框与它们一一对应，切换区块时保留）。 */
  const userSearch = ref('')
  const roomSearch = ref('')
  /** '' | 'true' | 'false'（服务端只接受这三种，非法值它返回 400）。 */
  const roomPublic = ref('')

  /** 危险操作成功后的一句话反馈（列表会被重新拉取）。 */
  const notice = ref('')
  const noticeError = ref('')
  /** 正在执行的危险操作（房间码 / 用户 id）：用于禁用按钮并显示进行中态。 */
  const busyKey = ref('')

  /** 请求序号：迟到的响应不许覆盖最新一次的结果。 */
  let seq = 0

  const isDenied = computed(() => denied.value !== '')

  /**
   * 把服务端拒绝翻译成整页结论。
   *
   * 返回 true 表示"这次失败已经决定了整页的呈现"，调用方不必再写区块级错误。
   */
  function classify(err: unknown): boolean {
    const e = toHttpError(err)
    if (e.status === 403) {
      denied.value = 'forbidden'
      // 文案用服务端那一句（"需要管理员权限"），而不是自己再编一句 —— 前端重写权限文案
      // 只在最坏的情况下才需要，而"服务端说了什么"在排障时是无价的信息。
      deniedText.value = e.message
      return true
    }
    if (e.status === 401) {
      denied.value = 'unauthenticated'
      deniedText.value = e.message
      return true
    }
    if (e.status === 503) {
      denied.value = 'unavailable'
      deniedText.value = e.message
      return true
    }
    return false
  }

  /** 这一页是不是满的（审计没有 total，只能靠它判断"还有没有下一页"）。 */
  function pageFull<T>(page: Paged<T>): boolean {
    return page.items.length >= page.limit && page.limit > 0
  }

  /** "还有下一页"：有 total 时用 total 更准（最后一页刚好满时不会多出一个空页入口）。 */
  function hasNext<T>(page: Paged<T>): boolean {
    if (page.total > 0) return page.offset + page.items.length < page.total
    return pageFull(page)
  }

  function hasPrev<T>(page: Paged<T>): boolean {
    return page.offset > 0
  }

  async function loadUsers(offset = 0): Promise<void> {
    users.value.loading = true
    users.value.error = ''
    try {
      const page = await api.listUsers(auth.authedFetch, {
        search: userSearch.value.trim(),
        limit: ADMIN_PAGE_SIZE,
        offset,
      })
      users.value.items = page.items
      users.value.total = page.total
      users.value.offset = page.offset
      users.value.limit = page.limit || ADMIN_PAGE_SIZE
    } catch (err) {
      if (!classify(err)) users.value.error = toHttpError(err).message
    } finally {
      users.value.loading = false
    }
  }

  async function loadRooms(offset = 0): Promise<void> {
    rooms.value.loading = true
    rooms.value.error = ''
    try {
      const page = await api.listRooms(auth.authedFetch, {
        search: roomSearch.value.trim(),
        public: roomPublic.value,
        limit: ADMIN_PAGE_SIZE,
        offset,
      })
      rooms.value.items = page.items
      rooms.value.total = page.total
      rooms.value.offset = page.offset
      rooms.value.limit = page.limit || ADMIN_PAGE_SIZE
    } catch (err) {
      if (!classify(err)) rooms.value.error = toHttpError(err).message
    } finally {
      rooms.value.loading = false
    }
  }

  async function loadMetrics(): Promise<void> {
    metrics.value.loading = true
    metrics.value.error = ''
    try {
      metrics.value.data = await api.fetchMetrics(auth.authedFetch)
    } catch (err) {
      if (!classify(err)) metrics.value.error = toHttpError(err).message
    } finally {
      metrics.value.loading = false
    }
  }

  async function loadLogs(offset = 0): Promise<void> {
    logs.value.loading = true
    logs.value.error = ''
    try {
      const page: AdminLogPage = await api.fetchLogs(auth.authedFetch, {
        limit: ADMIN_PAGE_SIZE,
        offset,
      })
      logs.value.items = page.lines
      logs.value.total = page.total
      logs.value.offset = page.offset
      logs.value.limit = page.limit || ADMIN_PAGE_SIZE
    } catch (err) {
      if (!classify(err)) logs.value.error = toHttpError(err).message
    } finally {
      logs.value.loading = false
    }
  }

  async function loadAudit(offset = 0): Promise<void> {
    audit.value.loading = true
    audit.value.error = ''
    try {
      const page = await api.fetchAudit(auth.authedFetch, { limit: ADMIN_PAGE_SIZE, offset })
      audit.value.items = page.items
      // 审计响应**没有** total：这里保持 0，hasNext 会自动退回"本页是否满页"。
      audit.value.total = 0
      audit.value.offset = page.offset
      audit.value.limit = page.limit || ADMIN_PAGE_SIZE
    } catch (err) {
      if (!classify(err)) audit.value.error = toHttpError(err).message
    } finally {
      audit.value.loading = false
    }
  }

  /** 拉取当前区块（切换标签页与"重试"都走它）。 */
  async function loadSection(next: AdminSection = section.value): Promise<void> {
    if (isDenied.value) return
    seq += 1
    const mine = seq
    switch (next) {
      case 'users':
        await loadUsers(0)
        break
      case 'rooms':
        await loadRooms(0)
        break
      case 'metrics':
        await loadMetrics()
        break
      case 'logs':
        await loadLogs(0)
        break
      case 'audit':
        await loadAudit(0)
        break
      default:
        break
    }
    // 迟到的区块加载不覆盖结论（例如用户连点标签页）。
    if (mine !== seq) return
  }

  /**
   * 进入页面：先拉一个区块。
   *
   * **为什么用 users 做探针**：它是第一个区块，而且是"只有 admin 能拿到的响应"里最便宜的一个。
   * 403/401/503 会在这里被翻译成整页结论；成功则说明权限没问题，后续区块照常加载。
   */
  async function bootstrap(): Promise<void> {
    booting.value = true
    denied.value = ''
    deniedText.value = ''
    try {
      await loadSection(section.value)
    } finally {
      booting.value = false
    }
  }

  async function selectSection(next: AdminSection): Promise<void> {
    section.value = next
    notice.value = ''
    noticeError.value = ''
    await loadSection(next)
  }

  // ---------- 危险操作（界面负责二次确认；这里只负责发请求 + 刷新 + 反馈）----------

  /** 封禁 / 解封。成功后重新拉当前页，并给出一句成功反馈。 */
  async function setUserStatus(user: AdminUser, status: string, reason: string): Promise<boolean> {
    return mutate(`user:${user.id}:status`, async () => {
      await api.patchUser(auth.authedFetch, user.id, { status, reason })
      await loadUsers(users.value.offset)
      return `已${status === 'banned' ? '封禁' : '解封'} ${user.username}`
    })
  }

  /** 改角色。写端会拒绝"把自己降级"（403 FORBIDDEN），文案原样显示。 */
  async function setUserRole(user: AdminUser, role: string, reason: string): Promise<boolean> {
    return mutate(`user:${user.id}:role`, async () => {
      await api.patchUser(auth.authedFetch, user.id, { role, reason })
      await loadUsers(users.value.offset)
      return `已把 ${user.username} 的角色改为 ${role}`
    })
  }

  /** 强制关闭房间（房内连接会被服务端断开）。 */
  async function closeRoom(room: AdminRoom): Promise<boolean> {
    return mutate(`room:${room.roomId}:close`, async () => {
      await api.closeRoom(auth.authedFetch, room.roomId)
      await loadRooms(rooms.value.offset)
      return `已强制关闭房间 ${room.roomId}`
    })
  }

  /** 下架房间（幂等；成功即从公开列表消失）。 */
  async function unpublishRoom(room: AdminRoom): Promise<boolean> {
    return mutate(`room:${room.roomId}:unpublish`, async () => {
      await api.unpublishRoom(auth.authedFetch, room.roomId)
      await loadRooms(rooms.value.offset)
      return `已下架房间 ${room.roomId}`
    })
  }

  /**
   * 危险操作的公共壳：进行中标记（按对象分键，避免"关 A 房时 B 房的按钮也变灰"）、
   * 成功刷新 + 提示、失败原样显示服务端文案（**不**刷新列表：状态没变，刷新只会掩盖）。
   */
  async function mutate(key: string, run: () => Promise<string>): Promise<boolean> {
    if (busyKey.value !== '') return false
    busyKey.value = key
    notice.value = ''
    noticeError.value = ''
    try {
      notice.value = await run()
      return true
    } catch (err) {
      const e = toHttpError(err)
      // 危险操作遇到 403/401 同样按整页权限结论处理（例如会话在页面上停留期间被降级）。
      if (!classify(err)) noticeError.value = e.message
      return false
    } finally {
      busyKey.value = ''
    }
  }

  return {
    section,
    booting,
    denied,
    deniedText,
    isDenied,
    users,
    rooms,
    metrics,
    logs,
    audit,
    userSearch,
    roomSearch,
    roomPublic,
    notice,
    noticeError,
    busyKey,
    bootstrap,
    selectSection,
    loadSection,
    loadUsers,
    loadRooms,
    loadMetrics,
    loadLogs,
    loadAudit,
    setUserStatus,
    setUserRole,
    closeRoom,
    unpublishRoom,
    hasNext,
    hasPrev,
    pageFull,
  }
})
