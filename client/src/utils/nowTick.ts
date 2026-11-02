/**
 * 全站单一刻度（票 #60；词表 "RelativeTime"：全站**单一 30s tick**）。
 *
 * 为什么要有它：页面开着的时候，"3 分钟前"这类文案要**自己走**。如果每个时间组件
 * 各建一个 `setInterval`，那么一个有 200 条帖子的列表就是 200 个定时器
 * —— 那是真实可测的浪费（后台标签页里还会被浏览器降频，导致时间不准）。
 *
 * 所以刻度是**全局唯一**的：
 *   - 全站同时最多一个定时器（哪怕页面上有一百个 RelativeTime 组件）；
 *   - `document.hidden` 时**暂停**（后台标签页不烧电，也不做无用功）；
 *   - 回可见时**立刻补算**一次（后台待了 10 分钟，回来就说"10 分钟前"，
 *     而不是等下一个 30 秒才改字）；
 *   - 最后一个订阅者退订后**停表**（页面空了就没有定时器）。
 *
 * 组件侧**禁自建定时器**（票面验收）：时间组件只订阅这里，不许自己 `setInterval`。
 *
 * 本模块与 Vue 无关（纯 TS，可被 Node 直接测）；`composables/useNowTick.ts`
 * 是它的 Vue 包装（订阅/退订的生命周期绑定）。
 */

/** 刻度间隔：30 秒。 */
export const TICK_INTERVAL_MS = 30_000

/** 订阅者回调：收到**当前时刻**（毫秒）。 */
export type TickListener = (now: number) => void

/**
 * 刻度需要的环境（可注入，便于测试与 SSR 安全）。
 *
 * 浏览器端用 `defaultEnv()`；测试用假环境（可控时钟 + 可数定时器）。
 */
export interface TickEnv {
  /** 当前时刻（毫秒）。 */
  now: () => number
  /** 建表。返回句柄。 */
  setInterval: (fn: () => void, ms: number) => unknown
  /** 拆表。 */
  clearInterval: (handle: unknown) => void
  /** 页面是否不可见。 */
  isHidden: () => boolean
  /** 订阅可见性变化；返回退订函数。 */
  subscribeVisibility: (cb: () => void) => () => void
}

/** 浏览器默认环境。 */
export function defaultEnv(): TickEnv {
  if (typeof window === 'undefined' || typeof document === 'undefined') {
    // 非浏览器环境（Node 里跑测试/构建）：没有可见性概念，刻度退化成纯定时器。
    return {
      now: () => Date.now(),
      setInterval: (fn, ms) => setInterval(fn, ms),
      clearInterval: (handle) => clearInterval(handle as ReturnType<typeof setInterval>),
      isHidden: () => false,
      subscribeVisibility: () => () => {},
    }
  }
  return {
    now: () => Date.now(),
    setInterval: (fn, ms) => window.setInterval(fn, ms),
    clearInterval: (handle) => window.clearInterval(handle as number),
    isHidden: () => document.hidden,
    subscribeVisibility: (cb) => {
      document.addEventListener('visibilitychange', cb)
      return () => document.removeEventListener('visibilitychange', cb)
    },
  }
}

/** 刻度句柄（给订阅者用）。 */
export interface NowTick {
  /** 订阅刻度；返回退订函数。 */
  subscribe: (listener: TickListener) => () => void
  /** 订阅数 +1（与退订成对；订阅函数本身不自动计数）。 */
  acquire: () => void
  /** 订阅数 −1。归零时停表。 */
  release: () => void
  /** 当前时刻。 */
  now: () => number
}

/**
 * 造一个刻度。
 *
 * 引用计数：`acquire`/`release` 是给"谁还在屏幕上"记账的；
 * 订阅函数返回的退订函数**只**移除监听者，不动计数 —— 两件事分开，
 * 是为了避免"组件退订时顺手多减一次计数"这种看不见的 off-by-one。
 */
export function createNowTick(env: TickEnv): NowTick {
  const listeners = new Set<TickListener>()
  let handle: unknown = null
  let holders = 0
  let unsubscribeVisibility: (() => void) | null = null

  function notify(): void {
    const now = env.now()
    for (const listener of [...listeners]) listener(now)
  }

  function stopTimer(): void {
    if (handle === null) return
    env.clearInterval(handle)
    handle = null
  }

  function startTimer(): void {
    if (handle !== null) return
    if (env.isHidden()) return // 隐藏时**不建表**（这是"暂停"的实现）
    handle = env.setInterval(notify, TICK_INTERVAL_MS)
  }

  function onVisibilityChange(): void {
    if (env.isHidden()) {
      stopTimer()
      return
    }
    // 回可见：先**立刻补算**一次，再把表挂上。
    // 顺序很重要：先补算才能让界面马上显示正确的"多久之前"；
    // 若先建表，用户会先看到旧文案再等 30 秒才更新。
    notify()
    startTimer()
  }

  function ensureVisibilityWatch(): void {
    if (unsubscribeVisibility !== null) return
    unsubscribeVisibility = env.subscribeVisibility(onVisibilityChange)
  }

  function releaseVisibilityWatch(): void {
    if (unsubscribeVisibility === null) return
    unsubscribeVisibility()
    unsubscribeVisibility = null
  }

  return {
    subscribe(listener: TickListener): () => void {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
    acquire(): void {
      holders += 1
      if (holders === 1) {
        ensureVisibilityWatch()
        startTimer()
      }
    },
    release(): void {
      holders = Math.max(0, holders - 1)
      if (holders === 0) {
        stopTimer()
        releaseVisibilityWatch()
      }
    },
    now(): number {
      return env.now()
    },
  }
}

/**
 * 浏览器端的**全局唯一**刻度（模块级单例）。
 *
 * 页面里所有时间组件都订阅这一个 —— 这就是"全站单一刻度"的落点。
 */
let singleton: NowTick | null = null

/** 取全局刻度（首次调用时创建）。 */
export function globalNowTick(): NowTick {
  if (singleton === null) singleton = createNowTick(defaultEnv())
  return singleton
}
