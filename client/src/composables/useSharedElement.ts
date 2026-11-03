/**
 * 共享元素 morph 的**接线层**（票 #78）。
 *
 *   utils/sharedElement.ts            纯函数：解析契约、判定该不该 morph（可单测）
 *   composables/useSharedElement.ts   就是本文件：找元素、量矩形、造幽灵、跑 WAAPI
 *
 * # 为什么幽灵元素挂在 `<body>` 上，而不是直接动那两个元素
 *
 * 换页时新旧两页**同时在 DOM 里**（`<Transition>` 没有 `mode`，离场页脱离文档流由
 * `transition.css` 负责）。这意味着源元素所在的那一页正在 `translateX(-12px)` 淡出，
 * 目标元素所在的那一页正在 `translateX(+12px)` 淡入。
 *
 * 任何挂在其中一页里的动画都会被**祖先的 transform 与 opacity 一起带走**：
 * 飞过去的元素会跟着父容器淡出、跟着父容器位移，于是"共享"变成"两个东西各自抖了一下"。
 * 做成 `position: fixed` 的独立幽灵，是与页面 transform/opacity 完全解耦的唯一简单办法。
 *
 * # 为什么量目标矩形时要减掉祖先的位移
 *
 * `getBoundingClientRect()` 返回的是**施加过祖先 transform 之后**的盒子。morph 第一帧
 * 若把幽灵放到"目标位置 + 12px"，起飞点就错开一截 —— 而 440ms 里这 12px 看得见，
 * 整段看起来像"元素先被推了一下才开始飞"。所以读一次页面根的 computed transform，
 * 把 tx/ty 减掉。这不是补丁，它保证两端用同一套坐标口径。
 *
 * # 失败一律零等待
 *
 * 判定（`planSharedMorph`）是纯的、同步的、只看已经量好的数。任何一项不满足就直接放弃，
 * 页面过渡按原时长走完。**刻意不等**图片解码、不等组件挂载：一旦开始等，换页就被卡在
 * 未知长度的等待里，"零等待降级"这条规则也就失效了。
 *
 * 源图片是否已解码在**离开页量矩形的那一刻**就一并读掉（那时它一定还在），
 * 而不是等目标出现后再回头看 —— 那时旧页可能已经淡出甚至被移除。
 */
import { nextTick } from 'vue'
import type { Router } from 'vue-router'
import { useRouteTransition } from './useRouteTransition'
import {
  MORPH_DURATION_MS,
  SHARED_ATTR,
  parseSharedId,
  planSharedMorph,
  type MorphRect,
} from '../utils/sharedElement'

/** 离开页量到的一份快照。 */
interface SourceSnapshot {
  /** 契约原值（`kind:key`）：匹配只按它，不按"页对页"。 */
  readonly raw: string
  readonly rect: MorphRect
  /** 源元素里的图片是否已解码（没有图片 ⇒ true）。 */
  readonly imageReady: boolean
  /** 克隆出来当幽灵用的节点（脱离文档的副本，不影响原树）。 */
  readonly template: HTMLElement
}

/** 本次换页待匹配的源快照；`null` = 没东西要飞。 */
let pendingSources: Map<string, SourceSnapshot> | null = null
/** 当前在飞的幽灵。 */
let ghost: HTMLElement | null = null
/** 本次被藏起来的真实目标元素（飞完要还原）。 */
let hidden: { el: HTMLElement; prev: string }[] = []

/** `getBoundingClientRect()` → 纯数据。 */
function toRect(el: Element): MorphRect {
  const box = el.getBoundingClientRect()
  return { x: box.left, y: box.top, width: box.width, height: box.height }
}

/**
 * 元素里（含自身）的图片是否都解码完了。
 *
 * `complete` 在"加载失败"时也是 true，所以要配合 `naturalWidth`：失败时返回 `false`
 * ⇒ 降级 ⇒ 落到字母头/纯色块的不动，比飞一块空框好。
 */
function isImageReady(el: Element): boolean {
  const images: HTMLImageElement[] = []
  if (el.tagName === 'IMG') images.push(el as HTMLImageElement)
  for (const img of Array.from(el.querySelectorAll('img'))) images.push(img)
  for (const img of images) {
    if (!img.complete || img.naturalWidth === 0) return false
  }
  return true
}

/** 读页面根当前的 transform 平移量；读不到就是"没有位移"，返回 0。 */
function rootTranslation(root: Element | null): { tx: number; ty: number } {
  if (!root) return { tx: 0, ty: 0 }
  const raw = getComputedStyle(root).transform
  if (!raw || raw === 'none') return { tx: 0, ty: 0 }
  const matched = raw.match(/matrix\(([^)]+)\)/)
  if (!matched) return { tx: 0, ty: 0 }
  const parts = matched[1].split(',').map((n) => Number(n.trim()))
  if (parts.length < 6 || parts.some((n) => Number.isNaN(n))) return { tx: 0, ty: 0 }
  return { tx: parts[4], ty: parts[5] }
}

/**
 * 元素是否在**会被换掉的那棵页面树**里（`#route-outlet` 的后代）。
 *
 * 这一条挡的是"外壳里的常驻元素"：顶栏头像也带着 `data-shared="avatar:<自己>"`，
 * 但顶栏在换页时**不重新挂载**，于是源与目标命中同一个节点 —— 幽灵会从原地飞到原地，
 * 看起来像"共享元素没动"，实测表现为 rect 两端完全相同（恒等 morph）。
 * 共享元素的前提是"这个元素在下一页会被**新的实例**取代"，所以源必须在被换掉的那棵树里。
 */
function insideRouteStage(el: Element, stage: Element | null): boolean {
  return stage !== null && stage !== el && stage.contains(el)
}

/** 元素是否落在**正在离场**的那一页里（`*-leave-active` 是 `transition.css` 的标记）。 */
function insideLeavingPage(el: Element): boolean {
  let node: Element | null = el
  while (node) {
    if (/-(leave-active|leave-from|leave-to)\b/.test(node.className || '')) return true
    node = node.parentElement
  }
  return false
}

/** 元素所在的页面根（`.route-stage` 的直接子元素）；找不到就是它自己。 */
function pageRootOf(el: Element, stage: Element | null): Element | null {
  if (!stage) return null
  let node: Element | null = el
  while (node && node.parentElement !== stage) node = node.parentElement
  return node
}

/** 把元素藏起来（用 visibility：改 `display` 会让后续布局与尺寸跟着变）。 */
function hideElement(el: HTMLElement): void {
  hidden.push({ el, prev: el.style.visibility })
  el.style.visibility = 'hidden'
}

/** 还原所有被藏起来的元素。 */
function restoreHidden(): void {
  for (const { el, prev } of hidden) el.style.visibility = prev
  hidden = []
}

/** 抹掉幽灵。 */
function dropGhost(): void {
  ghost?.remove()
  ghost = null
}

/**
 * 给源元素做一份"幽灵模板"。
 *
 * 用 `cloneNode(true)` 而不是"拿图片地址自己重建一个 `<img>`"：三处共享元素长相并不一致
 * （帖封面是 `<img>`、头像是字母块叠图片、房间卡是整块卡头），克隆让每一种都自动长得对，
 * 不必在这里再写三套重建逻辑 —— 那三套迟早会和组件里的实现漂开。
 */
function cloneForGhost(el: HTMLElement, rect: MorphRect): HTMLElement {
  const clone = el.cloneNode(true) as HTMLElement
  clone.removeAttribute('id')
  for (const node of [clone, ...Array.from(clone.querySelectorAll('*'))]) {
    node.removeAttribute('data-testid')
    // 幽灵绝不能再成为匹配目标，否则下一次换页会把它当成真元素。
    node.removeAttribute(SHARED_ATTR)
    if (node instanceof HTMLElement) node.removeAttribute('tabindex')
    if (node instanceof HTMLElement) node.setAttribute('aria-hidden', 'true')
  }
  clone.style.margin = '0'
  clone.style.width = `${rect.width}px`
  clone.style.height = `${rect.height}px`
  clone.style.boxSizing = 'border-box'
  return clone
}

/** 造幽灵并插进 `<body>`，起始位置就是源矩形。 */
function mountGhost(template: HTMLElement, from: MorphRect): HTMLElement {
  const shell = document.createElement('div')
  shell.className = 'shared-ghost'
  shell.setAttribute('aria-hidden', 'true')
  shell.style.cssText = [
    'position:fixed',
    'left:0',
    'top:0',
    'margin:0',
    'pointer-events:none',
    // 高于 `.route-stage` 的 z-index 但低于骨架屏（2 是不相干的堆叠上下文，见 App.vue）。
    'z-index:70',
    'transform-origin:top left',
    'will-change:transform',
    `width:${from.width}px`,
    `height:${from.height}px`,
    `transform:translate(${from.x}px, ${from.y}px)`,
  ].join(';')
  shell.appendChild(template)
  document.body.appendChild(shell)
  return shell
}

/** 把幽灵从 `from` 飞到 `to`，结束后自行清理。 */
function flyGhost(shell: HTMLElement, from: MorphRect, to: MorphRect, durationMs: number): void {
  const animation = shell.animate(
    [
      { transform: `translate(${from.x}px, ${from.y}px) scale(1, 1)` },
      {
        transform:
          `translate(${to.x}px, ${to.y}px) ` +
          `scale(${to.width / from.width}, ${to.height / from.height})`,
      },
    ],
    { duration: durationMs, easing: 'cubic-bezier(0.2, 0.7, 0.2, 1)', fill: 'forwards' },
  )
  animation.finished
    .catch(() => {
      /* 被 cancel（又换页了）不是错误 */
    })
    .finally(() => {
      if (ghost === shell) {
        dropGhost()
        restoreHidden()
      }
    })
}

/** CSS 属性值里的引号转义（`querySelector` 的字符串字面量）。 */
function cssEscape(value: string): string {
  if (typeof CSS !== 'undefined' && typeof CSS.escape === 'function') return CSS.escape(value)
  return value.replace(/["\\]/g, '\\$&')
}

/**
 * 装上共享元素接线。
 *
 * 与 `installRouteTransitionGuard` 一样必须在 `app.use(router)` **之前**调用：
 * `beforeEach` 里量离开页的矩形，那时 DOM 还是旧的那一页、且**还没被**任何过渡 class
 * 动过，是最干净的一刻。
 */
export function installSharedElementMorph(router: Router): void {
  router.beforeEach(() => {
    // 上一轮还没收尾就再来一次（快速连点）：把幽灵与隐藏全部收回，
    // 否则会残留一块飞了一半的色块停在屏幕中央。
    dropGhost()
    restoreHidden()
    pendingSources = null
    if (typeof document === 'undefined') return true

    const map = new Map<string, SourceSnapshot>()
    const stage = document.getElementById('route-outlet')
    for (const el of Array.from(document.querySelectorAll(`[${SHARED_ATTR}]`))) {
      if (!(el instanceof HTMLElement)) continue
      // 外壳里的常驻元素不算源：换页时它不会被新实例取代，飞起来只是原地抖动。
      if (!insideRouteStage(el, stage)) continue
      if (insideLeavingPage(el)) continue
      const raw = el.getAttribute(SHARED_ATTR)
      if (parseSharedId(raw) === null) continue
      const rect = toRect(el)
      // 零尺寸的源此刻就丢掉：省得后面带着它到处比，理由也说不清。
      if (rect.width <= 0 || rect.height <= 0) continue
      map.set(raw as string, {
        raw: raw as string,
        rect,
        imageReady: isImageReady(el),
        template: cloneForGhost(el, rect),
      })
    }
    pendingSources = map.size > 0 ? map : null
    return true
  })

  router.afterEach(() => {
    const sources = pendingSources
    pendingSources = null
    if (sources) void runMorph(sources)
  })
}

/** 换页之后：找目标、判一次、飞或不动。 */
async function runMorph(sources: Map<string, SourceSnapshot>): Promise<void> {
  const decision = useRouteTransition().value.decision
  // 只有前进触发。后退时列表页正在重新拉数据、目标经常还不存在，
  // 硬做只会降级成闪一下 —— 所以这里连 DOM 都不摸。
  if (decision.kind !== 'forward') return

  await nextTick()
  // 一帧：让新页先挂上 `*-enter-from`（此时它正处在 +12px 的位置），
  // 同时仍在 440ms 的预算内 —— 不会让页面多等。
  await new Promise<void>((resolve) => {
    if (typeof requestAnimationFrame === 'function') requestAnimationFrame(() => resolve())
    else resolve()
  })

  const stage = document.getElementById('route-outlet')
  for (const [, source] of sources) {
    const selector = `[${SHARED_ATTR}="${cssEscape(source.raw)}"]`
    let target: HTMLElement | null = null
    for (const candidate of Array.from(document.querySelectorAll<HTMLElement>(selector))) {
      // 跳过离场页里的同名元素（同一篇帖的列表卡与详情头图会短暂同时在 DOM 里）。
      if (insideLeavingPage(candidate)) continue
      target = candidate
      break
    }
    if (!target) continue // 目标没渲染出来 ⇒ 这一对降级，其余对不受影响

    const root = pageRootOf(target, stage)
    const { tx, ty } = rootTranslation(root)
    const box = toRect(target)
    const to: MorphRect = { x: box.x - tx, y: box.y - ty, width: box.width, height: box.height }
    const plan = planSharedMorph(decision, {
      from: source.rect,
      to,
      imageReady: source.imageReady,
    })
    if (plan.kind !== 'morph') continue

    hideElement(target)
    const ghostEl = mountGhost(source.template, source.rect)
    ghost = ghostEl
    flyGhost(ghostEl, source.rect, to, plan.durationMs || MORPH_DURATION_MS)
    // 一次换页只飞一对：多对同时飞会让画面变成几块色块乱窜。
    return
  }

  restoreHidden()
}

/** 供真机脚本与源码级判据读取的时长口径。 */
export function sharedMorphDuration(): number {
  return MORPH_DURATION_MS
}
