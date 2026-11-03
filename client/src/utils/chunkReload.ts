/**
 * chunk 加载失败时的**单次**自动刷新（2026-11-02 线上事故的收尾）。
 *
 * # 它解决什么
 *
 * 路由是懒加载的（票 #79）：每个页面的代码是一个独立的 `/assets/<Page>-<hash>.js`。
 * 而 `index.html` 是 `no-cache`、`/assets/*` 是 `immutable` —— 用户开着旧标签页时，
 * 手里的旧 index.html 引用的 chunk 名（哈希）在新构建里**已经不存在**了
 * （`client/scripts/prune-dist.mjs` 会删掉孤儿产物）。
 * 于是点开那个分区就是：
 *
 *   GET /assets/MePage-CCciaxB9.js → 404 (net::ERR_ABORTED)
 *
 * 表现是"点了没反应"或空白页，而**刷新一下就好了**（新 index.html 指向新 chunk）。
 * 这不是某一处代码的 bug，而是"部署了新版 + 用户没刷新"这个组合的必然结果，
 * 唯一的解法就是：发现这种情况时替用户刷新一次。
 *
 * # 为什么只刷一次
 *
 * 如果刷新之后仍然 404（构建本身就是残的、或 CDN 回源异常），无限刷新会变成
 * 一个用户关不掉的循环 —— 那比"一次空白页"糟得多。所以有**两道**闸：
 *   1. 时间闸：距上次自动刷新不足 `CHUNK_RELOAD_COOLDOWN_MS` 就不刷；
 *   2. 会话闸：`sessionStorage` 里的计数超过 `CHUNK_RELOAD_MAX` 就不刷（兜底）。
 * 两道都不过时**只记一条警告**，把决定权交回用户（他可以手动刷新）。
 *
 * # 为什么挂在 `vite:preloadError` 上
 *
 * Vite 的动态 import 走它自己的 `__vitePreload` 包装：加载失败时它在 window 上派发
 * `vite:preloadError` 事件（`event.preventDefault()` 可以吃掉默认的抛错）。
 * 直接监听这个事件，比在 `router.onError` 里靠文案匹配 "Failed to fetch dynamically
 * imported module" 稳得多 —— 后者是浏览器各自的措辞，且同样会被其它错误污染。
 * `router.onError` 另有用途（换页骨架的在途计数，见 `utils/routeLoadState.ts`），
 * **不要**在那儿抢。
 */

/** 自动刷新的冷却时间（ms）：这段时间内已经刷过一次就不再刷。 */
export const CHUNK_RELOAD_COOLDOWN_MS = 10_000

/** 同一会话最多自动刷新几次（时间闸之外的兜底，防"刷新后仍然坏"的循环）。 */
export const CHUNK_RELOAD_MAX = 2

/** `sessionStorage` 里记录"上次自动刷新时刻"与"次数"的键。 */
const STORAGE_KEY = 'pr:chunk-reload'

interface ReloadRecord {
  /** 上次自动刷新的时刻（`Date.now()`）。 */
  at: number
  /** 本会话已经自动刷新的次数。 */
  count: number
}

/**
 * 该不该为一次 chunk 加载失败自动刷新？**纯函数**，便于单测。
 *
 * @param now      当前时刻（`Date.now()`）
 * @param record   会话里记的上一状态；`null` 表示本会话还没刷过
 * @returns 决策与原因（原因只用于日志，不影响分支）
 */
export function decideChunkReload(
  now: number,
  record: ReloadRecord | null,
): { reload: boolean; reason: 'first' | 'cooldown' | 'too-many' } {
  if (record === null || record.count <= 0) return { reload: true, reason: 'first' }
  if (record.count >= CHUNK_RELOAD_MAX) return { reload: false, reason: 'too-many' }
  // `record.at > now` 只可能来自系统时钟回拨：那时冷却判据没有意义，
  // 按"冷却中"处理（少刷一次总比刷屏好）。
  if (now - record.at < CHUNK_RELOAD_COOLDOWN_MS) return { reload: false, reason: 'cooldown' }
  return { reload: true, reason: 'first' }
}

function readRecord(): ReloadRecord | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (raw === null) return null
    const parsed = JSON.parse(raw) as Partial<ReloadRecord>
    const at = typeof parsed.at === 'number' ? parsed.at : 0
    const count = typeof parsed.count === 'number' ? parsed.count : 0
    return { at, count }
  } catch {
    // sessionStorage 不可用（隐私模式）或内容坏了：当作"没刷过"。
    return null
  }
}

function writeRecord(record: ReloadRecord): void {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(record))
  } catch {
    // 写不进去不影响本次刷新，只是下次还会再刷一次（最多 `CHUNK_RELOAD_MAX` 次）。
  }
}

/**
 * 装上监听。**幂等**：重复调用只装一次（`main.ts` 里只调一次，但真机脚本可能重进）。
 *
 * @param reload 执行刷新的动作；默认 `location.reload()`。测试与验收脚本可替换它，
 *               从而在不真刷新的前提下断言"决策 + 只刷一次"。
 */
export function installChunkReloadGuard(reload: () => void = () => location.reload()): void {
  window.addEventListener('vite:preloadError', (event) => {
    // 吃掉 Vite 的默认抛错：我们要自己决定是刷新还是放弃。
    // 不 preventDefault 的话，控制台里会多一条未捕获异常，而它对本问题没有信息量。
    event.preventDefault()

    const now = Date.now()
    const record = readRecord()
    const decision = decideChunkReload(now, record)

    if (!decision.reload) {
      // 只警告、不刷新：让用户自己决定。这条日志是排查"为什么这次没有自动恢复"的唯一线索。
      console.warn(
        `[chunk-reload] 静态资源加载失败，但本次不自动刷新（原因：${decision.reason}）。` +
          '请手动刷新页面；若刷新后仍失败，说明部署的产物不完整。',
      )
      return
    }

    writeRecord({ at: now, count: (record?.count ?? 0) + 1 })
    console.warn('[chunk-reload] 静态资源加载失败（多半是部署了新版而页面还是旧的），自动刷新一次')
    reload()
  })
}
