/**
 * 列表/详情页共用的错误分诊（#26 为帖子三页写，wayfinder #28 提升为通用件）。
 *
 * 为什么现在搬出来：好友域的页面需要**完全相同**的五种分诊（整域未上线 / 401 / 403 /
 * 429 / 其它），而"401 与 403 必须分开""跳登录要带 redirect""服务端文案原样透出"
 * 这三条纪律已经是三张票反复验证过的结论。抄第二份的代价是：下次修其中一条，另一份会
 * 悄悄落后，而这种落后只在下一次 401 时才暴露。
 *
 * 分诊表（每一步都有不同的下一步动作）：
 *
 *   | 错误 | 用户该看到什么 | 该做什么 |
 *   | :--- | :--- | :--- |
 *   | 整域 404 | 「该分区后端尚未上线」空态 | 给重试（`CapabilityNotice` 自带） |
 *   | 401 | 「需要登录」 | `ensureLoggedIn()` 跳登录页，**带 redirect** |
 *   | 403 | 「没有权限做这件事」 | 原地说明，**不跳登录**（票 #23 的同一结论） |
 *   | 429 | 「操作太频繁」 | 原地说明 + 稍后重试 |
 *   | 其它 | 服务端文案原样 | 原地说明 |
 *
 * `FeatureUnavailable`（域在线、单个动作没接口）**不进这张表**：它不是失败，
 * 页面自己要看的是"这个按钮能不能用"，所以由调用方 `catch` 自己的那一句，
 * 这里不替它决定 (见 `api/mode.ts` 的说明)。
 */
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { HttpError, NETWORK_MESSAGE, toHttpError } from '../../../api/http'
import { isCapabilityUnavailable } from '../../../api/mode'
import { useAuthStore } from '../../../stores/auth'

/** 分诊结果：调用方据此决定渲染哪一块。 */
export type ApiFailure =
  | { kind: 'capability' }
  | { kind: 'unauthorized' }
  | { kind: 'forbidden'; message: string }
  | { kind: 'ratelimited'; message: string }
  | { kind: 'error'; message: string }

/** 一页的分诊状态 + 一个 `run()` 包装器。 */
export function useApiFailure(redirectPath: () => string) {
  const router = useRouter()
  const auth = useAuthStore()

  /** 非空的失败态（null = 没有失败）。 */
  const failure = ref<ApiFailure | null>(null)

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
        // 登录后送回原处：这些页面是浏览态，回来时应该还在那一页。
        void router.push({ name: 'login', query: { redirect: redirectPath() } })
      }
      return null
    }
  }

  return { failure, run }
}

/** 错误分诊（纯函数，便于单测；不依赖 Vue 运行时）。 */
export function classify(err: unknown): ApiFailure {
  if (isCapabilityUnavailable(err)) return { kind: 'capability' }

  const e = toHttpError(err)
  switch (e.status) {
    case 401:
      return { kind: 'unauthorized' }
    case 403:
      // 服务端文案原样：好友域最常见的两条是"对方已将你拉黑"与"你已拉黑对方"。
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
export function isUnauthorized(f: ApiFailure | null): boolean {
  return f?.kind === 'unauthorized'
}

export { HttpError }
