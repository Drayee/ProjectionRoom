/**
 * 互动计数的显示层缩写（票 #63）。
 *
 * ## 为什么是一个独立纯函数
 *
 * 「赞 0 / 收藏 0 / 评论 0」这类数字在动作条里每个按钮都有一位，三处都自己写
 * `count > 999 ? ... : count` 的话，下一次改档位就会漏掉其中一处。票 #63 的判据
 * 原文是「计数 0 隐数字、1.2k/1.2w 缩写」——这是一条**显示层规则**，所以它属于
 * `utils/`，不属于任何组件。
 *
 * ## 四档（与真机脚本 verify-authorrow.mjs 的 R4 对齐）
 *
 * | 输入 | 输出 | 说明 |
 * | :--- | :--- | :--- |
 * | 0 / 负数 | `''` | 空串：组件据此把整个计数位隐藏（不是显示「0」） |
 * | 1 – 999 | 原样 | 三位数不加单位 |
 * | 1000 – 9999 | `1.2k` | 千，一位小数 |
 * | ≥ 10000 | `1.2w` | 万，一位小数 |
 *
 * ## 为什么截断而不是四舍五入
 *
 * 9999 四舍五入会变成「10k」，而它还没到一万 —— 把没到的数说大，比说小更容易
 * 让人以为数据错了。截断的代价是 9999 显示成「9.9k」，那仍然是诚实的。
 *
 * 整数不带 `.0`（1000 → `1k` 而不是 `1.0k`）：`.0` 只增加噪声，不增加信息。
 */
export function formatCount(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return ''
  const value = Math.floor(n)
  if (value <= 0) return ''
  if (value < 1000) return String(value)
  if (value < 10000) return withUnit(value, 1000, 'k')
  return withUnit(value, 10000, 'w')
}

/**
 * 按单位换算并截断到一位小数。
 *
 * 截断走「先放大 10 倍取整、再缩小」而不是 `toFixed(1)`：`toFixed` 是四舍五入，
 * 会把 9999 变成 10.0k（见上）。浮点误差在这一步之前就被 `Math.floor` 吃掉了。
 */
function withUnit(value: number, unit: number, suffix: string): string {
  const scaled = Math.floor((value / unit) * 10) / 10
  // Number.isInteger 判断的是**换算后**的值：1000/1000 = 1 是整数，所以不补 `.0`。
  const text = Number.isInteger(scaled) ? String(scaled) : scaled.toFixed(1)
  return text + suffix
}

/**
 * 计数是否应该显示出来（0 与负数不显示）。
 *
 * 组件里需要一个布尔判断来加 `data-hidden`（真机脚本 R4b 读这个属性），
 * 所以把它也放在这里 —— 与 `formatCount` 同一个真源，不会两边判据漂移。
 */
export function hasCount(n: number): boolean {
  return formatCount(n) !== ''
}
