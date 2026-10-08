import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import * as api from '../api/auth'
import { AuthApiError, toAuthError } from '../api/auth'
import type { AuthSession, AuthUser, LoginInput, RegisterInput } from '../api/auth'
// 说明：router.ts → views/*.vue → stores/auth.ts 构成一个环。这里只在**函数体内**
// 引用 `router`（懒到真正要跳转的时刻才解析绑定），模块初始化期不碰它 ——
// 所以环不会在求值阶段被打开（ESM 的 live binding 在调用时已经全部就绪）。
import { router } from '../router'

/**
 * access token 的键。
 *
 * **只准进 sessionStorage**（规格 §5 / 审计 must）：它 15 分钟过期、服务端可用
 * `token_version` 一键作废，但一旦落到 localStorage 就等于"无 TLS + 存量 XSS 面下的账号单点失守"
 *（审计原话）。sessionStorage 的额外好处是**跟着标签页走**：关掉标签页即消失。
 */
const ACCESS_KEY = 'pr:access'

/**
 * 档案快照的键（localStorage）。
 *
 * 产品要求"本地缓存用户信息"，这里缓存的只有**非敏感、只用于显示**的字段：
 * 用户名、昵称、邮箱、角色、未读数。
 * ⚠️ 它**绝不是授权依据**（规格不变量 I3）：授权 100% 在服务端，角色只决定"显示什么"。
 * 任何将来想用 `profile.role === 'admin'` 决定"能不能做某件事"的代码都是错的 ——
 * 前端隐藏只能是体验优化，服务端必须仍然拒绝。
 */
const PROFILE_KEY = 'pr:profile'

export type AuthStatus = 'anonymous' | 'restoring' | 'authenticated'

/** 本地缓存的档案 = 服务端的 user + 本地派生的未读数。 */
export interface AuthProfile extends AuthUser {
  /**
   * 未读私聊数。三期（好友/私聊 + `/ws/user`）落地后才会有非零值；
   * 现阶段恒为 0，只是把这层缓存的形状一次定好，避免将来改 localStorage 的 schema。
   */
  unread: number
}

// ---------- 存储读写（都自己兜异常：隐私模式下 sessionStorage 可能直接抛错）----------

function readAccessToken(): string {
  try {
    return sessionStorage.getItem(ACCESS_KEY) ?? ''
  } catch {
    return ''
  }
}

function writeAccessToken(token: string): void {
  try {
    if (token === '') {
      sessionStorage.removeItem(ACCESS_KEY)
    } else {
      sessionStorage.setItem(ACCESS_KEY, token)
    }
  } catch {
    // 存不进去就退化成"只在内存里"：本次会话照常可用，只是刷新页面要重新登录。
    // 绝不因为存储不可用而中断登录流程。
  }
}

/**
 * 读本地档案缓存。
 *
 * localStorage 是**用户可改**的：解析失败、字段类型不对、缺用户名，一律当作"没有缓存"。
 * 反过来若把脏数据放进响应式状态，页头会渲染出 `undefined` 之类的垃圾。
 */
function readStoredProfile(): AuthProfile | null {
  let raw: string | null = null
  try {
    raw = localStorage.getItem(PROFILE_KEY)
  } catch {
    return null
  }
  if (!raw) return null

  let parsed: unknown = null
  try {
    parsed = JSON.parse(raw) as unknown
  } catch {
    return null
  }
  if (!parsed || typeof parsed !== 'object') return null

  const value = parsed as Partial<AuthProfile>
  const username = typeof value.username === 'string' ? value.username : ''
  if (username === '') return null

  return {
    id: typeof value.id === 'number' ? value.id : 0,
    username,
    displayName: typeof value.displayName === 'string' && value.displayName !== '' ? value.displayName : username,
    email: typeof value.email === 'string' ? value.email : '',
    role: typeof value.role === 'string' ? value.role : 'user',
    status: typeof value.status === 'string' ? value.status : 'active',
    unread: typeof value.unread === 'number' && value.unread > 0 ? value.unread : 0,
  }
}

function writeStoredProfile(profile: AuthProfile | null): void {
  try {
    if (profile === null) {
      localStorage.removeItem(PROFILE_KEY)
    } else {
      localStorage.setItem(PROFILE_KEY, JSON.stringify(profile))
    }
  } catch {
    // 同上：缓存写不进去不影响功能，只影响"下次打开时的免密恢复"。
  }
}

export const useAuthStore = defineStore('auth', () => {
  /**
   * access token（内存里的**唯一**一份，另一个副本在 sessionStorage 供刷新页面用）。
   * 初值直接取 sessionStorage：刷新页面时同步就位 → 判据③ 的"仍在登录态"不会先闪一下未登录。
   */
  const accessToken = ref(readAccessToken())
  const profile = ref<AuthProfile | null>(readStoredProfile())

  /**
   * 三态：anonymous / restoring / authenticated。
   * `restoring` 只出现在"没有 token 但有档案缓存"的首屏（等一次 refresh 出结果），
   * 界面据此显示"恢复登录中…"，而不是先把人头像抹掉再变回来。
   */
  const status = ref<AuthStatus>(
    accessToken.value !== '' ? 'authenticated' : profile.value !== null ? 'restoring' : 'anonymous',
  )

  /** 账号层面的提示（目前只有"登出请求没送达服务端"这一条），成功动作会清空它。 */
  const notice = ref('')

  const isLoggedIn = computed(() => status.value === 'authenticated')
  const restoring = computed(() => status.value === 'restoring')
  /** 显示用昵称：优先 displayName，其次 username，都没有就是空串（匿名）。 */
  const displayName = computed(() => profile.value?.displayName || profile.value?.username || '')

  function applyUser(user: AuthUser): void {
    const next: AuthProfile = { ...user, unread: profile.value?.unread ?? 0 }
    profile.value = next
    writeStoredProfile(next)
  }

  /** 一次成功的登录/注册/刷新之后的统一落地。 */
  function applySession(session: AuthSession): void {
    accessToken.value = session.accessToken
    writeAccessToken(session.accessToken)
    applyUser(session.user)
    status.value = 'authenticated'
  }

  /**
   * 清空本机登录态（判据⑤：不残留任何可用的 access token）。
   *
   * 三处一起清：内存 ref、sessionStorage、localStorage 里的档案。
   * 少清任何一处，"登出"就只是看起来登出了。
   */
  function clearSession(): void {
    accessToken.value = ''
    writeAccessToken('')
    profile.value = null
    writeStoredProfile(null)
    status.value = 'anonymous'
  }

  /**
   * 静默刷新：用 HttpOnly Cookie 里的 refresh token 换一枚新的 access token。
   *
   * 返回值表示"现在是否处于登录态"；失败时**已经**清空了本机登录态，但**不跳转** ——
   * 跳不跳由调用方决定：启动引导要静默（用户只是打开了首页），
   * 而业务请求的 401 要跳登录（用户正在做一个需要登录的动作）。
   */
  async function refreshSession(): Promise<boolean> {
    try {
      applySession(await api.refresh())
      return true
    } catch {
      clearSession()
      return false
    }
  }

  async function login(input: LoginInput): Promise<void> {
    notice.value = ''
    applySession(await api.login(input))
  }

  async function register(input: RegisterInput): Promise<void> {
    notice.value = ''
    applySession(await api.register(input))
  }

  /**
   * 登出（判据⑤）。
   *
   * 先通知服务端撤销 refresh 会话，再清本地；**服务端调用失败也照样清本地** ——
   * "用户以为退出了、本机却还留着可用 token"比"服务端会话多活一会儿"严重得多。
   * 但也不装成功：失败时在 `notice` 里说清"服务端会话可能仍然有效"。
   */
  async function logout(): Promise<void> {
    notice.value = ''
    try {
      await api.logout()
    } catch (err) {
      const e = toAuthError(err)
      notice.value = `已清除本地登录状态，但登出请求未送达服务端（${e.message}）：服务端会话可能仍然有效。`
    }
    clearSession()
  }

  /** 跳登录页，带上**当前路径**作为回跳目标（判据②）。 */
  function redirectToLogin(): Promise<void> {
    const current = router.currentRoute.value
    if (current.name === 'login' || current.name === 'register') {
      // 已在登录页：再跳只会把 redirect 摞成 /login?redirect=/login?redirect=…
      return Promise.resolve()
    }
    return router.push({ name: 'login', query: { redirect: current.fullPath } }).then(() => undefined)
  }

  /**
   * "这个动作需要登录"的**就地拦截**（判据②）。
   *
   * true = 可以继续；false = 已经把人送去登录页，调用方必须立刻 return（不要再发请求）。
   * 为什么不加全局路由守卫：授权 100% 在服务端（规格 §11），守卫只能控"看到什么"；
   * 而且观众进房**不需要登录**，全局守卫会顺手把那扇门关上。
   */
  function ensureLoggedIn(): boolean {
    if (isLoggedIn.value) return true
    void redirectToLogin()
    return false
  }

  /** 启动引导内部只跑一遍的锁。 */
  let bootstrapRun: Promise<void> | null = null

  /**
   * 启动引导（每次页面加载调用一次，内部保证只跑一遍）。
   *
   * 三种输入 → 三种结果：
   *   1. sessionStorage 里有 access token（刷新页面 / 同一标签页内跳转）：
   *      同步就当已登录，再**后台**校一次 `/me` 顺手刷新档案。
   *   2. 没有 token 但有 `pr:profile`（新标签页、或 sessionStorage 被清）：
   *      用它触发一次 `POST /api/auth/refresh` —— refresh token 在 HttpOnly Cookie 里，
   *      Cookie 还在就能**免密恢复登录态**（判据③ 验的就是这条）。
   *   3. 两者都没有：匿名，一个请求都不发（后端没上线时也不能多打一发）。
   *
   * 失败一律**静默**：用户可能只是打开了首页，绝不能因为"会话过期"就把人拽去登录页。
   * 需要跳登录的是另外两条路：用户主动做需登录的动作、或业务请求 401 且刷新失败。
   */
  function bootstrap(): Promise<void> {
    if (bootstrapRun === null) {
      bootstrapRun = runBootstrap()
    }
    return bootstrapRun
  }

  async function runBootstrap(): Promise<void> {
    try {
      if (accessToken.value !== '') {
        status.value = 'authenticated'
        try {
          applyUser(await api.me(accessToken.value))
        } catch (err) {
          const e = toAuthError(err)
          // 只有明确 401（过期/被撤销/被封禁）才值得去换新 token。网络不可达、
          // 或者后端这套接口还没上线（404）都必须**保留**本地登录态：
          // 否则一次抖动就会把用户"登出"，还顺手删掉了他的档案缓存。
          if (e.status === 401) {
            await refreshSession()
          }
        }
        return
      }

      if (profile.value === null) {
        status.value = 'anonymous'
        return
      }

      // 有档案没 token：正是"免密恢复"要覆盖的场景。
      await refreshSession()
    } catch {
      // 引导跑在页面加载路径上：任何意外都不许冒泡到 main.ts。
      status.value = accessToken.value === '' ? 'anonymous' : 'authenticated'
    }
  }

  function withToken(init: RequestInit, token: string): RequestInit {
    const headers = new Headers(init.headers)
    if (token !== '') {
      headers.set('Authorization', `Bearer ${token}`)
    }
    return { credentials: 'same-origin', ...init, headers }
  }

  /** 401 之后重试一次的开关（见 authedFetch 的说明）。 */
  interface AuthedFetchOptions {
    /**
     * 刷新也失败时是否把人送去登录页（默认 true）。
     *
     * 房间内的链路要传 false：把主播从房间页拽到登录页等于**顺手掐掉整个房间**，
     * 那比"这一次请求失败"严重得多 —— 它自己的失败路径（重建房间的重试 + 文案）已经够用。
     */
    redirectOnAuthFailure?: boolean
  }

  /**
   * 带 access token 的 fetch，401 时**自动刷新一次并重试一次**。
   *
   * 只重试一次是刻意的：刷新本身也会 401（Cookie 也没了 / 会话已被撤销）时再刷下去就是死循环；
   * 而且服务端的 refresh 带**重放检测**，无脑重试可能把该用户的全部会话都捅掉。
   * 重试仍然 401 时**原样把响应交给调用方**（由它决定怎么显示），不再自行重试。
   */
  async function authedFetch(
    input: string,
    init: RequestInit = {},
    options: AuthedFetchOptions = {},
  ): Promise<Response> {
    const first = await fetch(input, withToken(init, accessToken.value))
    if (first.status !== 401) {
      return first
    }

    const recovered = await refreshSession()
    if (!recovered) {
      if (options.redirectOnAuthFailure !== false) {
        void redirectToLogin()
      }
      throw new AuthApiError({
        status: 401,
        code: 'AUTH_EXPIRED',
        message: '登录状态已失效，请重新登录后再试',
      })
    }
    return fetch(input, withToken(init, accessToken.value))
  }

  return {
    // 状态
    accessToken,
    profile,
    status,
    notice,
    isLoggedIn,
    restoring,
    displayName,
    // 动作
    bootstrap,
    login,
    register,
    logout,
    authedFetch,
    ensureLoggedIn,
    redirectToLogin,
  }
})
