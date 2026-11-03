/**
 * 共享元素 morph 的**纯规划层**（票 #78）。
 *
 * # 分层纪律（与票 #76 同一套）
 *
 *   utils/sharedElement.ts            纯函数：解析契约、判定该不该 morph、给出时长
 *   composables/useSharedElement.ts   接线层：找元素、量矩形、造幽灵、跑 WAAPI
 *
 * 为什么判定必须纯：它是**产品规则**（"只有前进触发"、"失败即降级零等待"），
 * 必须能被 `node --test` 直接钉住。一旦它摸到 `getBoundingClientRect()`，
 * 就退化成只能靠真机脚本验的东西 —— 而"降级"这种**什么都没发生**的路径，
 * 在真机上恰恰是最难判的一条。
 *
 * # 契约：`data-shared="kind:key"`
 *
 * 两端只暴露这一个属性 + 一致的圆角/裁切，**不共享组件实例**
 * （决议 #45 §3 第 4 条：列表卡封面与详情头图尺寸差异大，实例化共享会撕）。
 *
 * 三处现役取值：
 *
 *   post-cover:<postId>   帖卡封面 → 详情头图
 *   avatar:<userId>       头像 → 档案页名片区
 *   room:<roomId>         房间卡 → 房页标题区
 *
 * **匹配不写死页对页**：离开页里任取一个 `data-shared`，到进入页里按**同一个值**
 * 找对端。这条来自原型期（`947371c`）的发现 —— 写死"帖子列表→帖子详情"会让
 * 「从 `/me` 的帖子块点进详情」这条同样成立的路径静默失效，而它没有任何报错。
 *
 * # 三条硬规则（决议 #45 §3）
 *
 *   1. **只有前进方向触发**（`kind === 'forward'`）。后退一律走方向性位移：
 *      列表页回来时正在重新拉数据，目标元素经常还不存在，硬做只会降级成闪一下。
 *   2. **失败即降级，零等待**：任一端找不到、图片未解码、矩形零尺寸 →
 *      当场放弃 morph，页面过渡**按原时长**（240ms）走完。
 *      "不许等"是关键：等一张图解码再决定，等于把换页卡在一个未知长度的等待里。
 *   3. **morph 440ms 与页面 240ms 并行**：两者同时开始，morph 会**比换页更晚结束**。
 *      这不是 bug，是叙事重点（元素比页面更显眼）。
 */
import type { TransitionDecision } from './routeTransition'

/** 共享元素契约的属性名（两端都只认它）。 */
export const SHARED_ATTR = 'data-shared'

/**
 * morph 时长（ms）。
 *
 * 与 `tokens.css` 的 `--dur-morph: 440ms` **同一条约定**（决议 #45 §3 第 3 条）。
 * CSS 侧那份供样式表用，本常量是 TS 侧的同一口径 —— 与 `TRANSITION_DURATION_MS`
 * 对 `--dur-comp` 的关系完全一样。
 */
export const MORPH_DURATION_MS = 440

/** 解析后的契约值。`kind` 只用于人读与源码级判据，匹配只按整个原值。 */
export interface SharedId {
  readonly kind: string
  readonly key: string
}

/**
 * 解析 `data-shared` 的值。
 *
 * 只按**第一个冒号**切：key 里允许再出现冒号（房间码、复合 id 都可能）。
 * 空值、缺冒号、冒号在首尾、任一侧为空白 —— 一律返回 `null`（当作"没声明"），
 * 而不是猜一个默认 kind：猜错会让两个本来无关的元素配成一对，
 * 症状是"点 A 结果 B 飞了出来"。
 */
export function parseSharedId(raw: string | null | undefined): SharedId | null {
  if (typeof raw !== 'string') return null
  const trimmed = raw.trim()
  const at = trimmed.indexOf(':')
  if (at <= 0 || at === trimmed.length - 1) return null
  const kind = trimmed.slice(0, at).trim()
  const key = trimmed.slice(at + 1).trim()
  if (!kind || !key) return null
  return { kind, key }
}

/** 一个元素在视口里的矩形（只留 morph 需要的四个数）。 */
export interface MorphRect {
  readonly x: number
  readonly y: number
  readonly width: number
  readonly height: number
}

/** 规划结果：要么飞，要么原地不动（页面过渡照旧）。 */
export type SharedMorphPlan =
  | { readonly kind: 'morph'; readonly durationMs: number; readonly reason: string }
  | { readonly kind: 'degrade'; readonly reason: string }

/** 规划入参：**全是量好的值**，不碰 DOM。 */
export interface SharedMorphInput {
  /** 离开页的源元素矩形；`null` = 没找到。 */
  readonly from: MorphRect | null
  /** 进入页的目标元素矩形；`null` = 没找到（含"页面上压根没渲染它"）。 */
  readonly to: MorphRect | null
  /**
   * 源元素里的图片是否**已经解码完成**。
   *
   * 缺省 `true`（没有图片的元素、或纯色块/字母头像）。
   * `false` 时降级：拿一张没解码的图去飞，飞的是**空白框**，
   * 而用户在 440ms 里看到的是一块空白色块从卡片飞到页头 —— 比不做还糟。
   */
  readonly imageReady?: boolean
}

/** 矩形是否"看得见"：宽高都必须是正数。 */
function hasArea(rect: MorphRect): boolean {
  return rect.width > 0 && rect.height > 0
}

/**
 * 判定这一对元素该不该 morph。
 *
 * 判定顺序 = 优先级：方向 → reduced-motion → 两端是否存在 → 尺寸 → 图片解码。
 * 顺序是有意义的：写反了会让"后退时因为目标缺失而降级"这类理由盖住真正的原因
 * （`reason` 是验收脚本与排查时唯一的解释来源）。
 */
export function planSharedMorph(
  decision: TransitionDecision,
  input: SharedMorphInput,
): SharedMorphPlan {
  if (decision.kind !== 'forward') {
    return {
      kind: 'degrade',
      reason: `只有前进触发共享元素（本次是 ${decision.kind}）`,
    }
  }
  if (decision.durationMs <= 0) {
    // 防御：forward 一定带时长，但万一决策器改了，宁可不动也不要飞一个 0ms 的幽灵。
    return { kind: 'degrade', reason: '决策没有时长：不做 morph' }
  }
  if (!input.from) {
    return { kind: 'degrade', reason: '离开页没有对应的 data-shared 元素' }
  }
  if (!input.to) {
    return { kind: 'degrade', reason: '进入页没有对应的 data-shared 元素（还没渲染出来）' }
  }
  if (!hasArea(input.from) || !hasArea(input.to)) {
    return { kind: 'degrade', reason: '任一端矩形尺寸为 0（隐藏/未布局）' }
  }
  if (input.imageReady === false) {
    return { kind: 'degrade', reason: '源图片尚未解码：飞过去只会是空白框' }
  }
  return {
    kind: 'morph',
    durationMs: MORPH_DURATION_MS,
    reason: `共享元素 ${MORPH_DURATION_MS}ms 上与页面 ${decision.durationMs}ms 并行`,
  }
}
