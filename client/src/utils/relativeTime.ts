/**
 * 相对时间的**唯一真源**（票 #60；词表 "Shared object components" 一节）。
 *
 * 六档（词表原文）：刚刚 / N 分钟前 / N 小时前 / N 天前 / 同年 M月D日 / 跨年 YYYY年M月D日。
 * **超 7 天回绝对日期** —— 那一档不再是相对说法，而是"哪天"。
 *
 * 纪律（与 `utils/avatar.ts` 同族）：
 *   - 页面里**不许**再写私有 `shortTime` / `timeAgo`。复制边界 = 复制 bug：
 *     两份实现一旦漂移，同一时刻在帖子页与消息页会显示不同的说法。
 *   - 本模块是**纯函数**：输入（原始串 + 基准时刻）→ 输出。时钟不归它管
 *     （时钟归 `composables/useNowTick.ts`，全站单一刻度）。
 *
 * 非法时间**原样透出**：不编一个假时间，也不吞掉它。用户看到 "昨天" 就说明后端
 * 给的就是 "昨天"，界面不该把它悄悄换成当前时间（那是在撒谎）。
 */

/** 一分钟 / 一小时 / 一天的毫秒数。 */
const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR
/** 相对档的上限：超过 7 天就回绝对日期。 */
const WEEK = 7 * DAY

/** 相对时间的完整视图（组件与页面都只认这一个形状）。 */
export interface TimeView {
  /**
   * 相对文案（六档）。
   *
   * 非法输入时是**原文**（原样透出）；空串也是空串（页面据此显示占位符）。
   */
  relative: string
  /**
   * 绝对日期：同年 `M月D日`、跨年 `YYYY年M月D日`。
   *
   * 相对档里它就是"那天是哪天"（给 title 之外的第二个信息位用）；
   * 绝对档里它**等于** `relative`。
   */
  date: string
  /** 精确绝对时间 `YYYY-MM-DD HH:mm`（本地时间）。给 title / aria-label。 */
  full: string
  /** `<time datetime>` 的值（ISO 8601）；非法时 `null`（此时不写该属性）。 */
  machine: string | null
  /** 原始串不是可解析的时间（页面据此决定要不要给 title / 绝对信息）。 */
  invalid: boolean
}

const pad = (n: number): string => String(n).padStart(2, '0')

/**
 * 绝对日期标签：同年只给月日，跨年才带年份。
 *
 * 判定看的是**该时刻自己的年份**与基准时刻年份是否相同 —— 不是"距今是否跨年"。
 */
function dateLabel(d: Date, now: Date): string {
  const month = d.getMonth() + 1
  const day = d.getDate()
  return d.getFullYear() === now.getFullYear()
    ? `${month}月${day}日`
    : `${d.getFullYear()}年${month}月${day}日`
}

/**
 * 精确绝对时间（本地时间分量，不做时区换算）。
 *
 * 格式 `YYYY-MM-DD HH:mm`：**不带秒** —— 决策票 #44 原文给的就是这个形状
 * （"title 与 aria-label 给本地时区的 YYYY-MM-DD HH:mm"）。带秒看着更"精确"，
 * 但悬停提示是给人核对的，分钟粒度已经够用，而秒会让"绝对时间"看起来像日志。
 */
function fullLabel(d: Date): string {
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}`
  )
}

/**
 * 把一个时间串解成相对时间的完整视图。
 *
 * @param raw   原始时间串（RFC3339 之类）。非法值原样透出。
 * @param nowMs 基准时刻（毫秒）。调用方传"当前时刻"，测试传固定值。
 *
 * 档位边界（表驱动用例见 `client/test/relative-time.test.mjs`）：
 *   - 差 < 60s          → 刚刚
 *   - 差 < 60min        → N 分钟前
 *   - 差 < 24h          → N 小时前
 *   - 差 < 7 天         → N 天前
 *   - 差 ≥ 7 天         → 绝对日期（同年 M月D日 / 跨年 YYYY年M月D日）
 *
 * ⚠ 7 天那一档的边界是 **< 7 天**（决策票 #44 的表原文）：恰好 7 天**已经**
 * 落进绝对档，"超 7 天回绝对日期"是它的子集表述。用 `<= WEEK` 会让
 * 恰好 7 天显示成「7 天前」—— 与表不符。
 *
 * 未来时刻按"刚刚"处理：客户端与服务端有时钟偏差，把未来显示成"−3 分钟前"是
 * 没人看得懂的文案，而"刚刚"至少不会误导。
 */
export function timeView(raw: string, nowMs: number): TimeView {
  const text = typeof raw === 'string' ? raw : String(raw ?? '')
  const d = new Date(text)
  const now = new Date(nowMs)

  if (Number.isNaN(d.getTime())) {
    // 非法时间原样透出：不给 datetime，也不编绝对时间。
    return { relative: text, date: text, full: text, machine: null, invalid: true }
  }

  const elapsed = nowMs - d.getTime()
  const abs = Math.abs(elapsed)

  let relative: string
  if (elapsed < 0) {
    relative = '刚刚'
  } else if (abs < MINUTE) {
    relative = '刚刚'
  } else if (abs < HOUR) {
    relative = `${Math.floor(abs / MINUTE)} 分钟前`
  } else if (abs < DAY) {
    relative = `${Math.floor(abs / HOUR)} 小时前`
  } else if (abs < WEEK) {
    relative = `${Math.floor(abs / DAY)} 天前`
  } else {
    relative = dateLabel(d, now)
  }

  return {
    relative,
    date: dateLabel(d, now),
    full: fullLabel(d),
    machine: d.toISOString(),
    invalid: false,
  }
}