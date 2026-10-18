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
import { HttpError, toHttpError } from './http'

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

/**
 * 域**在线**、但其中某一个动作服务端还没提供（wayfinder #28 引入）。
 *
 * 与 `CapabilityUnavailable` 的区别是"整域还是单点"，两者绝不能混：
 *   - `CapabilityUnavailable` → 页面整块换成「该分区后端尚未上线」空态；
 *   - `FeatureUnavailable`  → **该域照常工作**，只有这一个按钮/这一栏显示
 *     「服务端暂未提供此接口」。若把它也当成整域未上线，好友列表会因为
 *     "黑名单读不到"而整页消失 —— 用户会以为好友功能全废了。
 *
 * 为什么需要它而不是让真实实现返回空数组：空数组是**撒谎**（"你没有拉黑任何人"）
 * 与真实状态（"我不知道你拉黑了谁"）在界面上一模一样，而后者必须能被用户看见。
 *
 * 好友域曾经有三处单点缺口（按用户名找人 / 黑名单列表 / 解除拉黑，见 #28），
 * 已由 #39 在服务端补齐。这个类保留给**将来的**单点缺口用 ——
 * 判据仍然是"整域在线、但这个动作没有接口"。
 */
export class FeatureUnavailable extends Error {
  readonly domain: ApiDomain
  /** 用户可读的动作名，如「黑名单列表」。直接进界面文案。 */
  readonly feature: string

  constructor(domain: ApiDomain, feature: string, message?: string) {
    super(message ?? `服务端暂未提供「${feature}」接口`)
    this.name = 'FeatureUnavailable'
    this.domain = domain
    this.feature = feature
  }
}

/** 类型守卫：区分"整域未上线"与"这个动作还没接口"。 */
export function isFeatureUnavailable(err: unknown): err is FeatureUnavailable {
  return err instanceof FeatureUnavailable
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
 * （与 `/api/auth/*` 在账号关闭时的形态一致）。
 *
 * ⚠ **单条资源缺失也是 404**（`GET /api/posts/999`），那是另一件事：
 * 「这个帖子被删了 / id 写错了」必须显示成"内容不存在"，而不是"后端没上线"。
 * 两者都是 404，本函数分不出来 —— 因此**调用方要自己区分**：
 * 列表/详情这类"域肯定存在、只有这一条可能不在"的调用点，用
 * `toCapabilityUnavailableIf(domain, err, probe)` 把一次轻量探针一起给它，
 * 由探针（列表请求）回答"域到底在不在"。
 *
 * 为什么不让每个域实现各自写：这条判据的语义完全一样，而写错一次的代价是
 * 「删掉的帖子显示成该分区尚未上线」——用户会以为后端挂了，其实是内容没了。
 */
export function toCapabilityUnavailable(domain: ApiDomain, err: unknown): Error {
  if (err instanceof HttpError && err.status === 404) return new CapabilityUnavailable(domain)
  return err instanceof Error ? err : new Error(String(err))
}

/**
 * 带探针的版本：404 时先确认"整个域真的不在"，再判能力未上线。
 *
 * `probe` 必须是该域一个**必然存在**的读路径（如 `listPosts()`）。它只在前一个是
 * 404 时才被调用，因此正常路径上一次都不多打。
 *
 * 四种组合的期望（已用真实服务体验证过，不是推导）：
 *
 *   | 原始错误 | 探针结果 | 抛出 |
 *   | :--- | :--- | :--- |
 *   | 404 | 404 | `CapabilityUnavailable`（域没上线） |
 *   | 404 | 成功 | **原始错误**（内容不存在，如帖子被删） |
 *   | 404 | 非 404 失败 | 探针的错误（网络不可达 / 500，如实说） |
 *   | 非 404 | 不调用 | 原始错误（探针一次都不打） |
 *
 * 为什么探针失败不能猜成"域没上线"：那会把"服务端 500"说成"分区还没上线"，
 * 而后者暗示"以后再来看"，前者必须立刻报警。猜错方向会推迟真正的问题被发现。
 *
 * 注：本仓库**没有前端测试基建**（无 vitest / 无 test 脚本），所以这四种组合没有
 * 对应的单测——它们是上面真机验出来的，改这个函数前先按这张表手工复现一遍。
 */
export async function toCapabilityUnavailableIf(
  domain: ApiDomain,
  err: unknown,
  probe: () => Promise<unknown>,
): Promise<Error> {
  if (!(err instanceof HttpError && err.status === 404)) {
    return err instanceof Error ? err : new Error(String(err))
  }
  try {
    await probe()
  } catch (probeErr) {
    if (probeErr instanceof HttpError && probeErr.status === 404) {
      return new CapabilityUnavailable(domain)
    }
    // 探针自己也判成了"能力未上线"（它内部通常也包了一层 toCapabilityUnavailable）：
    // 直接认下来，别把它过成 toHttpError —— 那个函数只认 HttpError 形状，
    // 而 CapabilityUnavailable 会被压成 status:0 的"网络不可达"（真的踩过）。
    if (probeErr instanceof CapabilityUnavailable) {
      return probeErr
    }
    // 探针失败但不是 404（网络不可达 / 500）：如实说，别猜成"域没上线"。
    return toHttpError(probeErr)
  }
  return err
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
