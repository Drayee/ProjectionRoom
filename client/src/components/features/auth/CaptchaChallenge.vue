<script setup lang="ts">
/**
 * 人机验证题面（按顺序点出指定图形）。
 *
 * 从旧的独立窗口页（`pages/CaptchaPage.vue`）抽出来成为纯组件：
 * 它现在嵌在**页内子窗体**（`CaptchaDialog.vue`）里，而不是另一个浏览器窗口。
 *
 * 为什么改成页内子窗体（用户要求）：`window.open` 会被弹窗拦截器挡掉，
 * 而被挡时用户只能看到一句"请允许弹窗"——那是一次白跑。子窗体没有这个问题，
 * 且不打断当前的注册表单状态。
 *
 * 再说一次它的定位（见 useRegisterFlow 的说明）：这是**摩擦**，不是安全边界。
 * 题面与校验都在前端，读一遍这份源码就能绕过。真正的防线是服务端的
 * 验证码错误计数（落库）+ 发送冷却 + 每 IP/每邮箱限流。
 *
 * 通过之后 emit `pass`，由父级决定下一步（发验证码）。
 */
import { computed, onMounted, ref } from 'vue'

/** 可用的图形（全用 CSS 画，不依赖图标文件）。 */
type Shape = 'circle' | 'square' | 'triangle' | 'diamond' | 'cross' | 'ring'

const ALL_SHAPES: Shape[] = ['circle', 'square', 'triangle', 'diamond', 'cross', 'ring']

/** 需要按顺序点出的图形数量。 */
const TARGET_COUNT = 3
/** 每轮题面的格子数（3×3）。 */
const GRID = 9

interface Cell {
  id: number
  shape: Shape
}

const cells = ref<Cell[]>([])
/** 本题要求的点击顺序。 */
const order = ref<Shape[]>([])
/** 已按顺序点中的格子下标。 */
const picked = ref<number[]>([])
const wrong = ref(false)
const passed = ref(false)

const emit = defineEmits<{ (e: 'pass'): void }>()

function randInt(max: number): number {
  const c = typeof globalThis !== 'undefined' ? globalThis.crypto : undefined
  if (c && typeof c.getRandomValues === 'function') {
    const buf = new Uint32Array(1)
    c.getRandomValues(buf)
    return buf[0] % max
  }
  return Math.floor(Math.random() * max)
}

function shuffle<T>(list: T[]): T[] {
  const out = [...list]
  for (let i = out.length - 1; i > 0; i--) {
    const j = randInt(i + 1)
    ;[out[i], out[j]] = [out[j], out[i]]
  }
  return out
}

/** 出新题：挑 TARGET_COUNT 个**互不相同**的目标图形，随机撒进 3×3。 */
function newChallenge(): void {
  picked.value = []
  wrong.value = false
  passed.value = false

  // 目标互不相同：避免"同一个图形要点两次"的歧义（那会让判据变复杂）。
  const targets = shuffle(ALL_SHAPES).slice(0, TARGET_COUNT)
  order.value = [...targets]

  const distractors = ALL_SHAPES.filter((s) => !targets.includes(s))
  const layout: Shape[] = [...targets]
  while (layout.length < GRID) {
    layout.push(distractors[randInt(distractors.length)])
  }

  cells.value = shuffle(layout).map((shape, id) => ({ id, shape }))
}

/** 点某一格：必须正好是"顺序上的下一个目标"。 */
function pick(cell: Cell): void {
  if (passed.value) return
  const want = order.value[picked.value.length]
  if (cell.shape !== want) {
    wrong.value = true
    // 点错就重出一题：不给"逐步试错"的机会（那会把摩擦降到零）。
    window.setTimeout(newChallenge, 420)
    return
  }
  wrong.value = false
  picked.value.push(cell.id)
  if (picked.value.length === order.value.length) {
    passed.value = true
    // 延迟一拍再上报：让"通过"的成功脉冲真的能被看见（立刻 emit 会让子窗体
    // 在动画的第一帧就开始关闭，用户只看到闪一下）。
    window.setTimeout(() => emit('pass'), 280)
  }
}

function shapeLabel(s: Shape): string {
  switch (s) {
    case 'circle':
      return '圆形'
    case 'square':
      return '方形'
    case 'triangle':
      return '三角形'
    case 'diamond':
      return '菱形'
    case 'cross':
      return '十字'
    case 'ring':
      return '圆环'
  }
}

/** 已点进度（给父级做"还剩几步"的提示）。 */
const progress = computed(() => ({ done: picked.value.length, total: order.value.length }))

onMounted(newChallenge)

defineExpose({ reset: newChallenge })
</script>

<template>
  <div class="challenge">
    <p class="prompt">
      请按顺序点出：
      <span class="order">
        <template v-for="(s, i) in order" :key="`${s}-${i}`">
          <span class="order-item" :class="{ done: i < picked.length }">
            <span class="mini" :class="`shape-${s}`" aria-hidden="true" />
            <span class="sr">{{ shapeLabel(s) }}</span>
          </span>
        </template>
      </span>
    </p>

    <div class="grid" :class="{ wrong, done: passed }" role="group" aria-label="验证图形网格">
      <button
        v-for="cell in cells"
        :key="cell.id"
        class="cell"
        type="button"
        :disabled="passed"
        :class="{ hit: picked.includes(cell.id) }"
        :aria-label="`第 ${cell.id + 1} 格：${shapeLabel(cell.shape)}`"
        @click="pick(cell)"
      >
        <span class="shape" :class="`shape-${cell.shape}`" aria-hidden="true" />
      </button>
    </div>

    <p class="tip" aria-live="polite">
      <span v-if="passed" class="ok">验证通过，正在发送验证码…</span>
      <span v-else>已点 {{ progress.done }} / {{ progress.total }} · 点错会换一题</span>
    </p>
  </div>
</template>

<style scoped>
.challenge {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.prompt {
  margin: 0;
  font-size: 13px;
  color: var(--dim);
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.order {
  display: inline-flex;
  gap: 6px;
}

.order-item {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border: 1px solid var(--line);
  color: var(--accent);
}

/* 已点过的目标变绿：同一信息还有"已点 n / 总数"的文字，不只靠颜色 */
.order-item.done {
  border-color: var(--ok);
  color: var(--ok);
  background: rgba(61, 220, 151, 0.1);
}

.mini {
  width: 13px;
  height: 13px;
  display: inline-block;
  background: currentColor;
}

.grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
}

.grid.wrong {
  animation: shake 300ms ease;
}

@keyframes shake {
  0%,
  100% {
    transform: none;
  }
  25% {
    transform: translateX(-4px);
  }
  75% {
    transform: translateX(4px);
  }
}

.cell {
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--line);
  background: var(--surface);
  min-height: 64px;
  transition:
    border-color var(--dur-micro) var(--ease),
    background var(--dur-micro) var(--ease),
    transform var(--dur-micro) var(--ease);
}

.cell:hover:not(:disabled) {
  border-color: var(--accent);
  background: rgba(180, 108, 255, 0.08);
  /* 抬起 2px：格子在指尖下"浮起"，比只改边框更容易被看见 */
  transform: translateY(-2px);
}

.cell:active:not(:disabled) {
  transform: translateY(0) scale(0.95);
}

/* 点中目标：一次快速放大回弹（一次性动画，不是常驻循环） */
.cell.hit {
  border-color: var(--ok);
  background: rgba(61, 220, 151, 0.12);
  animation: cell-pop 260ms var(--ease);
}

@keyframes cell-pop {
  0% {
    transform: scale(0.9);
  }
  55% {
    transform: scale(1.06);
  }
  100% {
    transform: scale(1);
  }
}

.cell:disabled {
  cursor: default;
}

/* 全部点中：整张网格一次成功脉冲（把"过了"这件事说清楚再关窗） */
.grid.done {
  animation: grid-pass 480ms var(--ease);
}

@keyframes grid-pass {
  0% {
    transform: scale(1);
  }
  40% {
    transform: scale(1.035);
  }
  100% {
    transform: scale(1);
  }
}

.shape {
  width: 30px;
  height: 30px;
  display: block;
  background: var(--text);
}

.shape-circle {
  border-radius: 50%;
}

.shape-ring {
  border-radius: 50%;
  background: transparent;
  border: 6px solid var(--text);
}

.shape-square {
  border-radius: 0;
}

.shape-diamond {
  transform: rotate(45deg);
}

.shape-triangle {
  background: transparent;
  width: 0;
  height: 0;
  border-left: 16px solid transparent;
  border-right: 16px solid transparent;
  border-bottom: 27px solid var(--text);
}

.shape-cross {
  background: transparent;
  position: relative;
}

.shape-cross::before,
.shape-cross::after {
  content: '';
  position: absolute;
  background: var(--text);
}

.shape-cross::before {
  left: 50%;
  top: 0;
  width: 9px;
  height: 100%;
  transform: translateX(-50%);
}

.shape-cross::after {
  top: 50%;
  left: 0;
  height: 9px;
  width: 100%;
  transform: translateY(-50%);
}

.mini.shape-ring {
  border-width: 3px;
}

.mini.shape-triangle {
  border-left-width: 7px;
  border-right-width: 7px;
  border-bottom-width: 12px;
}

.mini.shape-cross::before {
  width: 4px;
}

.mini.shape-cross::after {
  height: 4px;
}

.tip {
  margin: 0;
  font-size: 11px;
  color: var(--dim);
}

.tip .ok {
  color: var(--ok);
}

.sr {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}
</style>
