/**
 * 相对时间六档格式化（**全站唯一源，禁止复制**）。
 *
 * 为什么必须是唯一源：站内的"时间"文案本来是散在各页面手写的 —— `MessagesPage.vue` 一份
 * `shortTime`（今天 `HH:mm`，否则 `M/D HH:mm`），admin 三页各一份 `shortTime`，
 * `UserPage` 一份 `raw.slice(0, 10)`，帖子详情直接把 RFC 串原样打出来。四五份实现给出
 * 四五种口径：同一条消息在会话列表里显示"10/29 21:04"，在消息正文里显示
 * "2026-10-29T13:04:00Z"，用户只会以为系统算错了时间。
 * 把口径收进这一份**纯函数**（不 import Vue、不碰 DOM、只吃参数）之后，任何页面要相对时间
 * 都从这里取，谁也不再自己拼。
 *
 * 复制它会造成什么回归（这就是"禁止复制"要防的事）：拷贝一份很容易，但两份代码从此各自演化 ——
 * 有人给副本加了"超过 1 小时显示到秒"，有人给原件加了"未来时间原样透出"，于是同一屏上出现
 * 两种相对时间口径，而且**没有编译期提示能发现这种漂移**：两份都是类型正确的 TS，测试也各测各的。
 * 要改口径就改这里（全站一起变），不要在别处再写一份。
 *
 * 六档判据（delta = now - 目标时刻，`now` 默认 `Date.now()`）：
 *   1. 非法时间或空串 → 原样透出输入字符串（不抛错、不编一个假时间占位）；
 *   2. delta < 0（未来时间）→ 原样透出输入字符串（服务端时钟可能领先，不做校正）；
 *   3. delta < 1 分钟 → 「刚刚」；
 *   4. delta < 1 小时 → 「N分钟前」；
 *   5. delta < 1 天 → 「N小时前」；
 *   6. delta < 7 天 → 「N天前」；
 *   7. delta ≥ 7 天 → 绝对日期（同一自然年「M月D日」，跨年「YYYY年M月D日」；月/日不补零）。
 *
 * **不做时钟偏差校正**：服务端时间与本地时钟的差不是错误，是事实（用户机器的时钟可能不准，
 * 服务端也可能领先）。加偏移补偿会在"用户改过系统时间"时把整屏时间挪歪 —— 那比显示差几秒更糟。
 * 这里一律以本地 `now` 为准，边界由 `test/script/verify-relativetime.mjs` 表驱动钉住。
 */

/**
 * 相对时间的刷新节拍（30 秒）。
 *
 * 相对时间是**会过期**的：页面停在"3分钟前"不动就永远停在"3分钟前"。
 * 页面侧应当每隔这个节拍重算一次（配合 `formatRelativeTime(iso, now)` 的显式 `now` 参数），
 * 而不是各页面自己挑一个刷新间隔 —— 挑不同间隔会让同屏文案在"刚刚/1分钟前"之间来回跳。
 */
export const RELATIVE_TIME_TICK_MS = 30_000

/** 一分钟（毫秒）。 */
const MINUTE_MS = 60_000
/** 一小时（毫秒）。 */
const HOUR_MS = 3_600_000
/** 一天（毫秒）。 */
const DAY_MS = 86_400_000
/** 相对档的天数上限：达到 7 天即走绝对日期档。 */
const RELATIVE_DAYS_LIMIT = 7

/**
 * 解析 ISO 时间串为毫秒时间戳；非法时间（含空串）返回 `null`。
 *
 * 返回 `null` 而不是 `NaN`：调用方要判断的是"这个时间能不能用"，`NaN` 会顺着算术
 * 悄悄流进比较运算里（`NaN < 60000` 恒为 false），把一个坏值变成"看起来有结果"的输出。
 */
export function parseTimestamp(iso: string): number | null {
  if (typeof iso !== 'string' || iso.trim() === '') return null
  const ts = Date.parse(iso)
  return Number.isFinite(ts) ? ts : null
}

/**
 * 相对时间文案（六档）。
 *
 * `now` 显式可传：这是**测试的抓手**，也让调用方能用自己的节拍时钟统一驱动；
 * 省略时用 `Date.now()`。
 */
export function formatRelativeTime(iso: string, now: number = Date.now()): string {
  const ts = parseTimestamp(iso)
  if (ts === null) return iso

  const delta = now - ts
  // 未来时间原样透出：服务端时钟领先不是错误，纠正它会引入更坏的偏差。
  if (delta < 0) return iso
  if (delta < MINUTE_MS) return '刚刚'
  if (delta < HOUR_MS) return `${Math.max(1, Math.floor(delta / MINUTE_MS))}分钟前`
  if (delta < DAY_MS) return `${Math.max(1, Math.floor(delta / HOUR_MS))}小时前`
  if (delta < RELATIVE_DAYS_LIMIT * DAY_MS) return `${Math.max(1, Math.floor(delta / DAY_MS))}天前`

  // 绝对日期档：自然年/自然日按**本地时区**判定（getFullYear 等）。
  const target = new Date(ts)
  const year = target.getFullYear()
  const month = target.getMonth() + 1
  const day = target.getDate()
  if (year === new Date(now).getFullYear()) {
    return `${month}月${day}日`
  }
  return `${String(year).padStart(4, '0')}年${month}月${day}日`
}

/**
 * 绝对时间文案：本地时区的「YYYY-MM-DD HH:mm」（年 4 位、月/日/时/分补零）。
 *
 * 同时用于元素的 `title` 与 `aria-label`：相对文案（「3分钟前」）对屏幕阅读器不友好，
 * 绝对文案才是可朗读、可核对的时刻。非法时间返回空串（此时调用方应退回相对文案）。
 */
export function formatAbsoluteTime(iso: string): string {
  const ts = parseTimestamp(iso)
  if (ts === null) return ''
  const d = new Date(ts)
  const y = String(d.getFullYear()).padStart(4, '0')
  const mo = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  return `${y}-${mo}-${day} ${hh}:${mm}`
}
