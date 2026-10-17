/**
 * 前端 mock/real 适配层的唯一开关（wayfinder #22）。
 *
 * 为什么要有这一层：本图（#21）的施工顺序是**前端先行、后端跟进**。每个分区在后端接口
 * 落地之前就得能开发、能验收，后端上线之后又要"只翻一个开关"就切到真实现。约定是：
 *
 *   `api/<域>.ts`（门面，唯一被页面 import 的出口）
 *     ├── `api/mock/<域>.ts`  本地 mock（数据 + 纯内存状态）
 *     └── `api/real/<域>.ts`  真实 HTTP 实现
 *
 * 门面在模块加载时按本文件的开关二选一，**导出形状两边完全一致**——页面永远不需要知道
 * 自己在跟谁说话。样板见帖子域：`api/posts.ts` / `api/mock/posts.ts` / `api/real/posts.ts`。
 *
 * 三个刻意的设计决定：
 *
 *   1. **默认全 mock**。只有显式写成"关"的取值（`0` / `false` / `off` / `no`）才翻成真实
 *      实现。反过来（默认真、显式开 mock）会让"忘了配环境变量"直接变成一堆 404 空态，
 *      而那正是这套机制要消除的症状。
 *   2. **整域 404 是一种"能力未上线"，不是错误**。真实实现拿到 404 时抛
 *      `CapabilityUnavailable`，页面据此渲染「该分区后端尚未上线」空态。这与
 *      `useAuthForm.ts` 把 `/api/auth/*` 的 404 解释成「账号能力未启用（游客模式）」是
 *      同一条既有约定（CLAUDE.md §4），不是新发明。
 *   3. **开关是构建期常量**。`import.meta.env.VITE_MOCK_*` 在打包时被 Vite 替换成字面量，
 *      运行期没有"读全局配置"这一步。代价是 mock 与 real 两份实现都会进产物（合计几 KB），
 *      换来的是零运行期配置注入面。
 */
import { HttpError } from './http'

/**
 * 六个可切换的域。
 *
 * 与 `client/.env.example` 的六个 `VITE_MOCK_*` 一一对应；新增域时两处都要加，否则
 * `SWITCHES` 的 `Record` 类型会立刻报缺键（故意的：漏一个就会静默沿用别的域的开关）。
 */
export type ApiDomain = 'posts' | 'friends' | 'messages' | 'forum' | 'official' | 'users'

/**
 * 整域能力尚未上线。
 *
 * 页面捕获它之后渲染降级空态（`components/ui/CapabilityNotice.vue`），而不是把
 * 「请求失败（HTTP 404）」这种裸文案糊给用户——后者看起来像整个后端挂了。
 */
export class CapabilityUnavailable extends Error {
  readonly domain: ApiDomain

  constructor(domain: ApiDomain, message?: string) {
    super(message ?? `该分区后端尚未上线（${domain}）`)
    this.name = 'CapabilityUnavailable'
    this.domain = domain
  }
}

/** 类型守卫：`catch (err)` 之后一行判断，不必记错误类名。 */
export function isCapabilityUnavailable(err: unknown): err is CapabilityUnavailable {
  return err instanceof CapabilityUnavailable
}

/** 把 `import.meta.env` 的取值收窄成 `string | undefined`（Vite 的类型是 `any`）。 */
function readRaw(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

/**
 * 开关语义：默认 mock，只有显式"关"才算真实实现。
 *
 * 空串 / 未定义 / 任意其它取值一律按 mock 处理——"配错了"退化成"还是 mock"，
 * 不会退化成"整片分区 404"。
 */
function readSwitch(raw: string | undefined): boolean {
  const value = (raw ?? '').trim().toLowerCase()
  const off = value === '0' || value === 'false' || value === 'off' || value === 'no'
  return !off
}

/**
 * 每个域一个取值函数（而不是一张 `Record<ApiDomain, string>` 常量表）。
 *
 * 写成函数是为了让 `import.meta.env.VITE_MOCK_POSTS` 保持**静态成员访问**——
 * Vite 只替换静态形式；写成 `import.meta.env[name]` 在构建期拿不到值。
 */
const SWITCHES: Record<ApiDomain, () => string | undefined> = {
  posts: () => readRaw(import.meta.env.VITE_MOCK_POSTS),
  friends: () => readRaw(import.meta.env.VITE_MOCK_FRIENDS),
  messages: () => readRaw(import.meta.env.VITE_MOCK_MESSAGES),
  forum: () => readRaw(import.meta.env.VITE_MOCK_FORUM),
  official: () => readRaw(import.meta.env.VITE_MOCK_OFFICIAL),
  users: () => readRaw(import.meta.env.VITE_MOCK_USERS),
}

/** 该域当前是否走本地 mock（默认 true）。 */
export function isMock(domain: ApiDomain): boolean {
  return readSwitch(SWITCHES[domain]())
}

/**
 * 真实实现的错误归一：整域路由不存在时转成 `CapabilityUnavailable`。
 *
 * 判定口径是 **HTTP 404**：`/api/posts/*` 这一组路由根本没注册，服务端返回的就是 404
 * （与 `/api/auth/*` 在账号关闭时的形态一致）。单条资源缺失（如 `GET /api/posts/999`）
 * 也是 404，那种情况由各域实现自己在调用点区分——它已经有 post id 的上下文，
 * 这里没有，硬猜只会把"帖子被删了"说成"后端没上线"。
 *
 * 各域 real 实现的用法：
 *
 *   try {
 *     return await requestJSON<Post[]>(authedFetch, '/api/posts')
 *   } catch (err) {
 *     throw toCapabilityUnavailable('posts', err)
 *   }
 */
export function toCapabilityUnavailable(domain: ApiDomain, err: unknown): Error {
  if (err instanceof HttpError && err.status === 404) return new CapabilityUnavailable(domain)
  return err instanceof Error ? err : new Error(String(err))
}

/**
 * 开关快照（诊断与验收脚本用，不参与渲染）。
 *
 * 生产构建里也保留：它不含任何凭据，只是六个布尔值，而"我现在到底在跟 mock 还是真实
 * 接口说话"是排查空态时第一个要回答的问题。
 */
export function mockSwitchSnapshot(): Record<ApiDomain, boolean> {
  return {
    posts: isMock('posts'),
    friends: isMock('friends'),
    messages: isMock('messages'),
    forum: isMock('forum'),
    official: isMock('official'),
    users: isMock('users'),
  }
}
