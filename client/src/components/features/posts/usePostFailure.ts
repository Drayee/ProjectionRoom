/**
 * 帖子三页共用的错误分诊（wayfinder #26）。
 *
 * 为什么抽出来而不是三页各写一遍：mock 时代的 `catch` 只需要分"能力未上线 / 失败"两种，
 * 切到真实接口之后多出三种，而**三种都要给出不同的下一步动作**：
 *
 *   | 错误 | 用户该看到什么 | 该做什么 |
 *   | :--- | :--- | :--- |
 *   | 整域 404 | 「该分区后端尚未上线」空态 | 给重试（`CapabilityNotice` 自带） |
 *   | 401 | 「需要登录」 | `ensureLoggedIn()` 跳登录页，**带 redirect** |
 *   | 403 | 「没有权限做这件事」 | 原地说明，**不跳登录**（票 #23 的同一结论） |
 *   | 429 | 「操作太频繁」 | 原地说明 + 稍后重试 |
 *   | 其它 | 服务端文案原样 | 原地说明 |
 *
 * 三个刻意的约定：
 *
 *  1. **401 与 403 必须分开**。把人拽去登录页解决不了 403 —— 他明明是登录的。
 *     这条与 `api/admin.ts` / 管理端三视图是同一条纪律（票 #23 踩过）。
 *  2. **跳转带 `redirect`**。否则用户登录完被丢到首页，还得自己找回刚才在看什么。
 *     约定由调用方传当前路由（`useRoute().fullPath`），这里不自己猜。
 *  3. **服务端文案原样透出**。`toHttpError` 已经保证 message 是给用户看的最终表述，
 *     这里不改写；只有"根本没有服务端文案"时才用本地兜底。
 */
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { HttpError, NETWORK_MESSAGE, toHttpError } from '../../../api/http'
import { isCapabilityUnavailable } from '../../../api/mode'
import { useAuthStore } from '../../../stores/auth'

/** 分诊结果：调用方据此决定渲染哪一块。 */
export type PostFailure =
  | { kind: 'capability' }
  | { kind: 'unauthorized' }
  | { kind: 'forbidden'; message: string }
  | { kind: 'ratelimited'; message: string }
  | { kind: 'error'; message: string }

/** 一页的分诊状态 + 一个 `run()` 包装器。 */
export function usePostFailure(redirectPath: () => string) {
  const router = useRouter()
  const auth = useAuthStore()

  /** 非空的失败态（null = 没有失败）。 */
  const failure = ref<PostFailure | null>(null)

  /**
   * 把一次调用包进分诊。
   *
   * 返回 true = 成功；false = 失败（`failure` 已置好，调用方直接 return 即可）。
   * 401 会**先跳转再返回 false**，所以调用方绝不能继续发请求。
   */
  async function run<T>(fn: () => Promise<T>): Promise<T | null> {
    failure.value = null
    try {
      return await fn()
    } catch (err) {
      failure.value = classify(err)
      if (failure.value.kind === 'unauthorized') {
        // 登录后送回原处：帖子是浏览态，回来时应该还在那一页。
        void router.push({ name: 'login', query: { redirect: redirectPath() } })
      }
      return null
    }
  }

  return { failure, run }
}

/** 错误分诊（纯函数，便于单测；不依赖 Vue 运行时）。 */
export function classify(err: unknown): PostFailure {
  if (isCapabilityUnavailable(err)) return { kind: 'capability' }

  const e = toHttpError(err)
  switch (e.status) {
    case 401:
      return { kind: 'unauthorized' }
    case 403:
      // 服务端文案原样：最常见的两条是"只能删除自己的评论"与"登录状态失效"。
      return { kind: 'forbidden', message: e.message }
    case 429:
      return { kind: 'ratelimited', message: e.message }
    default:
      return { kind: 'error', message: e.offline ? NETWORK_MESSAGE : e.message }
  }
}

/**
 * 写操作前的登录闸门。
 *
 * 与 `run()` 的分工：这是**发请求之前**的本地判断（省掉一次注定 401 的往返），
 * `run()` 是**服务端真的拒绝之后**的兜底。两道都要有 —— 前者让登录态用户无感，
 * 后者覆盖"token 在本地看着有效、服务端已判失效"的窗口。
 */
export function ensureLoggedInForWrite(): boolean {
  return useAuthStore().ensureLoggedIn()
}

/** 供模板直接判断的便捷 getter（模板里不想写 `.value.kind`）。 */
export function isUnauthorized(f: PostFailure | null): boolean {
  return f?.kind === 'unauthorized'
}

export { HttpError }
