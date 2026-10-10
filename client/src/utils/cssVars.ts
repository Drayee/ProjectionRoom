import type { CSSProperties } from 'vue'

/**
 * 类型安全的 CSS 自定义属性（`--xxx`）。
 *
 * 为什么需要它：Vue 的 `CSSProperties` 刻意**去掉了索引签名**（换来闭合的样式类型检查），
 * 因此直接把 `{ '--aw-h': '440px' }` 丢给 `:style` 会报类型错。这里用一次受控的断言把
 * "只接受 `--` 开头的键"这个约束留在调用点上 —— 比全局给 `CSSProperties` 加索引签名
 * 更安全（那会让任何拼错的 CSS 属性都悄悄通过）。
 */
export function cssVars(vars: Record<`--${string}`, string | number>): CSSProperties {
  return vars as CSSProperties
}
