import { ref } from 'vue'
import type { Directive } from 'vue'

/**
 * 轻量 tooltip（`v-tip`）。
 *
 * 为什么自己做一个而不是继续用原生 `title`：原生提示有两个硬缺陷 —— ①各家浏览器的
 * 出现延迟不可控（通常 0.5~1.5 秒），"图标按钮"这一层几乎没有反馈；②位置固定在元素
 * 下方，密集的行内图标（顶栏一排图标、FAB 三个圆钮）会指向错误的那个。
 * 这里要的是"鼠标在哪、提示就跟到哪"，且键盘 `focus` 也能触发。
 *
 * 三条纪律：
 *   1. **提示不是唯一信息源**（可访问性底线）：每个用 `v-tip` 的控件必须另有
 *      `aria-label`（给读屏）。图层本身 `aria-hidden="true"`，不参与读屏播报 ——
 *      否则同一个意思会被念两遍（`aria-label` 一遍、提示一遍）。
 *   2. **只跟随指针，不抢事件**：图层 `pointer-events: none`，绝不吞掉底下按钮的点击。
 *   3. **停在地图内**：位置按视口夹取，避免贴边时撑出横向滚动条。
 *
 * 用法：
 *   v-tip="'刷新列表'"                 ← 静态文案
 *   v-tip="failed ? '重试（上次失败）' : '重试'"   ← 动态文案（组件重渲染时同步）
 */

interface TipState {
  /** 提示文案（空串 = 不显示）。 */
  text: string
  /** 视口坐标（CSS px）。 */
  x: number
  y: number
  visible: boolean
}

/** 全局提示状态：整个应用只有一份图层，所有触发点写它。 */
export const tipState = ref<TipState>({ text: '', x: 0, y: 0, visible: false })

/** 触发元素 → 最新文案。用 WeakMap 是为了"组件重渲染时同步动态文案"，且不泄漏 DOM。 */
const textOf = new WeakMap<HTMLElement, () => string>()

/** 触发元素 → 卸载时的清理函数（否则监听器会跟着 DOM 一起泄漏）。 */
const cleanupOf = new WeakMap<HTMLElement, () => void>()

/** 指针偏移：贴着光标的右下角，既不遮住光标也不遮住图标中心。 */
const POINTER_OFFSET = 14
/** 视口夹取的边距。 */
const EDGE = 12

/** 由 TooltipLayer 在滚轮/窗口尺寸变化时调用：位置已经失真，直接收起。 */
export function hideTip(): void {
  if (tipState.value.visible) {
    tipState.value = { ...tipState.value, visible: false }
  }
}

function clamp(value: number, max: number): number {
  if (!Number.isFinite(value)) return EDGE
  return Math.max(EDGE, Math.min(value, Math.max(EDGE, max - EDGE)))
}

function showAt(text: string, x: number, y: number): void {
  if (text === '') return
  const viewportW = typeof window === 'undefined' ? 0 : window.innerWidth
  const viewportH = typeof window === 'undefined' ? 0 : window.innerHeight
  tipState.value = {
    text,
    // 夹取用"光标位置"的量纲：真正不溢出的保证来自图层自身的 max-width + translate 半宽修正，
    // 这里只负责不让它飞出视口。
    x: clamp(x, viewportW),
    y: clamp(y, viewportH),
    visible: true,
  }
}

/**
 * 绑定一个元素。
 *
 * 事件选择：
 *   - `mouseenter` / `mousemove`：前者让它**立刻**出现（不等 1 秒），后者让它跟随；
 *   - `mouseleave`：立刻消失；
 *   - `focus` / `blur`：键盘用户（Tab）也能看到同样的提示，位置取元素包围盒，
 *     而不是臆造一个 (0,0)；
 *   - `click`：点下去就把提示收掉 —— 动作已经发生了，提示继续挂着只会挡住结果。
 */
export const tipDirective: Directive<HTMLElement, string> = {
  mounted(el, binding) {
    textOf.set(el, () => (typeof binding.value === 'string' ? binding.value : ''))

    const text = (): string => textOf.get(el)?.() ?? ''

    const onEnter = (event: MouseEvent): void => {
      showAt(text(), event.clientX + POINTER_OFFSET, event.clientY + POINTER_OFFSET)
    }
    const onMove = (event: MouseEvent): void => {
      // 已经显示时只挪位置，避免每次 mousemove 都重建对象。
      if (tipState.value.visible && tipState.value.text === text()) {
        tipState.value = { ...tipState.value, x: clamp(event.clientX + POINTER_OFFSET, window.innerWidth), y: clamp(event.clientY + POINTER_OFFSET, window.innerHeight) }
        return
      }
      onEnter(event)
    }
    const onLeave = (): void => hideTip()
    const onFocus = (): void => {
      const rect = el.getBoundingClientRect()
      showAt(text(), rect.left + rect.width / 2, rect.bottom + 6)
    }
    const onClick = (): void => hideTip()

    el.addEventListener('mouseenter', onEnter)
    el.addEventListener('mousemove', onMove)
    el.addEventListener('mouseleave', onLeave)
    el.addEventListener('focus', onFocus)
    el.addEventListener('blur', onLeave)
    el.addEventListener('click', onClick)

    cleanupOf.set(el, () => {
      el.removeEventListener('mouseenter', onEnter)
      el.removeEventListener('mousemove', onMove)
      el.removeEventListener('mouseleave', onLeave)
      el.removeEventListener('focus', onFocus)
      el.removeEventListener('blur', onLeave)
      el.removeEventListener('click', onClick)
    })
  },

  /** 动态文案（`v-tip="expr"`）：模板里表达式变了就换掉取值函数。 */
  updated(el, binding) {
    textOf.set(el, () => (typeof binding.value === 'string' ? binding.value : ''))
  },

  unmounted(el) {
    cleanupOf.get(el)?.()
    cleanupOf.delete(el)
    textOf.delete(el)
    // 触发元素在提示还开着的时候被卸载（例如点了按钮切走了视图）：顺手收掉，避免留一个孤儿提示。
    hideTip()
  },
}

declare module 'vue' {
  export interface GlobalDirectives {
    vTip: typeof tipDirective
  }
}
