<!--
  相对时间（全站唯一源，票 #60）。

  契约：
    · 文案 = 六档（刚刚 / N 分钟前 / N 小时前 / N 天前 / 同年 M月D日 / 跨年 YYYY年M月D日），
      超过 7 天回绝对日期；
    · `title` 与 `aria-label` 给**精确绝对时间**（悬停可见，可核对）；
    · `<time datetime>` 给机器可读值；
    · 非法时间**原样透出**（不编假时间），且不写 datetime；
    · **不自建定时器**：文案靠全站单一刻度（`composables/useNowTick.ts`）驱动。

  为什么 title 与 aria-label 都是精确绝对时间：相对文案是"人读"的，
  绝对时间是"核对"的 —— 两者都要有，但只有绝对时间值得占 aria-label
  （读屏用户拿到的是"10月22日 08:30:00"，不是"8 天前"这种需要心算的说法）。

  归 ui/：纯展示，不读 store / router（props 进、无事件出）。
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useNowTick } from '../../composables/useNowTick'
import { timeView } from '../../utils/relativeTime'

const props = withDefaults(
  defineProps<{
    /** 原始时间串（RFC3339 之类）。非法值原样透出。 */
    value: string
    /** 空值时显示的占位符（默认 '—'）。 */
    placeholder?: string
    /**
     * 是否给 title / aria-label。
     *
     * 默认 true。关闭它只影响**辅助信息**，文案与 datetime 不变。
     */
    showAbsolute?: boolean
    /**
     * 把**可见文案**也换成精确绝对时间（票 #71 加的）。
     *
     * 默认 false = 六档相对文案（"3 分钟前"）。
     * true  = `YYYY-MM-DD HH:mm`（本地时区）。
     *
     * 为什么需要这个开关而不是让页面自己格式化：全站时间格式化只有这一份实现
     * （`utils/relativeTime.ts` 的 `timeView`）。设置页的"加入时间"要的是
     * "哪一天注册的" —— "刚刚"对一个刚注册的账号毫无信息量，而它恰恰是这个字段
     * 最需要说清的时刻。让页面自己拼日期就等于开第二个格式化出口。
     */
    absolute?: boolean
  }>(),
  { placeholder: '—', showAbsolute: true, absolute: false },
)

/** 全站单一刻度：页面开着数字自己走（30s 一跳，后台暂停）。 */
const now = useNowTick()

const view = computed(() => timeView(props.value, now.value))

/** 空值 → 占位符；非法值 → 原样；`absolute` 时给精确绝对时间。 */
const text = computed(() => {
  if (props.value === '' || props.value === null || props.value === undefined) return props.placeholder
  if (view.value.invalid) return view.value.relative
  return props.absolute ? view.value.full : view.value.relative
})

/** 非法时间不给绝对信息（title 与 aria-label 都省掉）。 */
const absoluteInfo = computed(() => (view.value.invalid ? null : view.value.full))

/** 精确绝对时间（给 title / aria-label）。 */
const absoluteLabel = computed(() => {
  if (!props.showAbsolute) return undefined
  return absoluteInfo.value ?? undefined
})
</script>

<template>
  <time
    class="relative-time"
    :datetime="view.machine ?? undefined"
    :title="absoluteLabel"
    :aria-label="absoluteLabel"
    data-testid="relative-time"
  >
    {{ text }}
  </time>
</template>

<style scoped>
/*
 * 换字**不靠动画**：内容变了就变了。
 *
 * 为什么不用 transition/keyframes：reduced-motion 下"属性值必须仍变化"是全站纪律
 * （词表 Natural layer），而这里干脆一个动画都不做 —— 没有动画可降级，也就没有
 * "降级后不换字"的回归形态。改字是内容更新，不是动效。
 */
.relative-time {
  /* 时间戳是次要信息：不抢行内其它内容的注意力，但保持可读。 */
  color: inherit;
  font-variant-numeric: tabular-nums;
}
</style>
