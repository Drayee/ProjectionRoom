<script setup lang="ts">
/**
 * 落地页（票 #10 B 定稿 · 票 #13 动效规格）——morph 单状态机重写。
 *
 * 两态：hero（月喵标题 + 「开始」按钮）↔ auth（登录窗）。
 * /login、/register 复用本组件：initial-stage='auth' 直接以展开态挂载。
 *
 * 设计要点（全部由 .open 一个 class 驱动，无任何过渡编排 timer）：
 *   1. 卡片布局恒为窗口尺寸 —— morph 全程零布局变化，滚动条永不跳动；
 *   2. hero 态把卡片整体 scale(0.4, 0.16) 缩成按钮。「开始」标签用恒定反比
 *      scale(2.5, 6.25) 抵消，起点文字尺寸精确等于原字号（160ms 内淡出，膨胀不可察觉）；
 *   3. 按钮与窗口同为直角矩形（B 全直角语言）——morph 期间 border-radius 恒为 0，
 *      矩形直接连续长成窗口，无需任何圆角切换；
 *   4. 窗口内容不做反比缩放——它只在卡片接近终态时才淡入（压扁 ≤15% 且半透明），
 *      展开方向「内容随窗口成型浮现」、收起方向「内容压入按钮」，双向对称自然；
 *      （反比缩放与卡片缩放缓动不同步，中段会把内容鼓胀到 2 倍高，已实测排除）
 *   5. 「开始」标签与窗口内容始终共存于 DOM，只切 opacity，无“消失再出现”；
 *   6. 标题只走 transform 缩小上移，不参与布局。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AuthPanel from '../auth/AuthPanel.vue'
import JoinRoomCard from '../room/JoinRoomCard.vue'
import type { AuthTab } from '../auth/useAuthForm'

const props = withDefaults(
  defineProps<{
    initialStage?: 'hero' | 'auth'
    initialTab?: AuthTab
    directEntry?: boolean
  }>(),
  { initialStage: 'hero', initialTab: 'login', directEntry: true },
)

/** 页面唯一形态开关：hero（按钮）↔ auth（窗口）。 */
const stage = ref<'hero' | 'auth'>(props.initialStage)
/** 窗口内的面板：登录表单 ↔ 直接进房。 */
const panel = ref<'auth' | 'direct'>('auth')
/** 标题拼合入场态（组装完成后摘除，避免常驻 animation 列表超标）。 */
const assembling = ref(true)

const open = computed(() => stage.value === 'auth')
/** 直接以 auth 挂载的页面（/login、/register）没有可回退的 hero 形态，不可收起。 */
const canCollapse = props.initialStage === 'hero'
const windowTitle = computed(() => (panel.value === 'auth' ? '登录月喵' : '进入放映室'))

const cardEl = ref<HTMLElement | null>(null)

const displayName = (() => {
  try {
    return localStorage.getItem('pr:name') || ''
  } catch {
    return ''
  }
})()

let assembleTimer: ReturnType<typeof setTimeout> | undefined

function start(): void {
  stage.value = 'auth'
}

function collapse(): void {
  if (canCollapse) stage.value = 'hero'
}

/** 触发键只处理 hero 态：auth 态绝不 preventDefault，保证表单回车可提交。 */
function onTriggerKey(e: KeyboardEvent): void {
  if (stage.value !== 'hero') return
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    start()
  }
}

function onWindowKey(e: KeyboardEvent): void {
  if (e.key === 'Escape') collapse()
}

watch(stage, async (s) => {
  await nextTick()
  if (s === 'auth') {
    document.getElementById('auth-username')?.focus()
  } else {
    cardEl.value?.focus()
  }
})

onMounted(() => {
  assembleTimer = setTimeout(() => (assembling.value = false), 1100)
  window.addEventListener('keydown', onWindowKey)
})

onBeforeUnmount(() => {
  if (assembleTimer !== undefined) clearTimeout(assembleTimer)
  window.removeEventListener('keydown', onWindowKey)
})
</script>

<template>
  <div class="landing" :class="{ open }">
    <main class="stage">
      <header class="hero" :class="{ assembling }">
        <h1 class="brand" aria-label="月喵">
          <span class="base" aria-hidden="true">月喵</span>
          <span class="sl s1" aria-hidden="true">月喵</span>
          <span class="sl s2" aria-hidden="true">月喵</span>
          <span class="sl s3" aria-hidden="true">月喵</span>
        </h1>
      </header>

      <!-- morph 卡片：布局恒为窗口尺寸，hero 态整体缩成一个胶囊按钮 -->
      <div
        ref="cardEl"
        class="card"
        :class="{ open }"
        :role="open ? undefined : 'button'"
        :tabindex="open ? -1 : 0"
        :aria-label="open ? undefined : '开始'"
        :aria-expanded="open ? undefined : 'false'"
        aria-controls="landing-window"
        @click="stage === 'hero' && start()"
        @keydown="onTriggerKey"
      >
        <span class="start-label" aria-hidden="true">开始</span>

        <div id="landing-window" class="window">
          <div class="win-bar">
            <span class="win-title">{{ windowTitle }}</span>
            <button
              v-if="canCollapse"
              type="button"
              class="win-close"
              aria-label="收起登录窗"
              @click.stop="collapse"
            >
              ✕
            </button>
          </div>

          <Transition name="win" mode="out-in">
            <AuthPanel
              v-if="panel === 'auth'"
              key="auth"
              :initial-tab="props.initialTab"
              :direct-entry="props.directEntry"
              @direct="panel = 'direct'"
            />
            <div v-else key="direct" class="direct">
              <JoinRoomCard :name="() => displayName || '观众'" />
              <button class="back-auth" type="button" @click="panel = 'auth'">
                ← 返回登录
              </button>
            </div>
          </Transition>
        </div>
      </div>

      <p class="foot dim">看片不需要账号 · 有房间码就能进</p>
    </main>
  </div>
</template>

<style scoped>
/* ---------- 布局（morph 期间无任何布局属性变化 → 滚动条永不动） ---------- */
.landing {
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  flex-direction: column;
  padding: 24px 20px 28px;
}

.stage {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  justify-content: safe center;
  gap: 40px;
}

/* ---------- 标题：故障黑体 + 三片撕裂（票 #13 规格，周期互质） ---------- */
.hero {
  transform-origin: 50% 100%;
  transition: transform var(--dur-morph) var(--ease);
}

/* 展开时缩小上移：纯 transform，不参与布局 */
.landing.open .hero {
  transform: translateY(-44px) scale(0.52);
}

.brand {
  position: relative;
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(60px, 14vw, 132px);
  line-height: 1;
  letter-spacing: 0.04em;
  color: var(--text);
}

/* 基准层：只占位不上色，为 h1 提供尺寸（可访问名由 h1 的 aria-label 承担） */
.brand .base {
  visibility: hidden;
}

.brand .sl {
  position: absolute;
  left: 0;
  top: 0;
  width: 100%;
}

.brand .s1 {
  clip-path: inset(0 0 66% 0);
  color: var(--accent);
  animation: slB1 var(--glitch-slice-1) steps(3, end) infinite;
}

.brand .s2 {
  clip-path: inset(33% 0 33% 0);
  color: var(--glitch-p);
  animation: slB2 var(--glitch-slice-2) steps(2, end) infinite;
}

.brand .s3 {
  clip-path: inset(66% 0 0 0);
  color: var(--glitch-r);
  animation: slB3 var(--glitch-slice-3) steps(3, end) infinite;
}

@keyframes slB1 {
  0%, 80% { transform: none; opacity: 0.9; }
  84% { transform: translateX(-0.14em); opacity: 1; }
  88%, 100% { transform: none; opacity: 0.9; }
}

@keyframes slB2 {
  0%, 86% { transform: none; opacity: 0.85; }
  90% { transform: translateX(0.11em); }
  94%, 100% { transform: none; opacity: 0.85; }
}

@keyframes slB3 {
  0%, 90% { transform: none; opacity: 0.95; }
  93% { transform: translateX(-0.07em) skewX(-6deg); }
  96%, 100% { transform: none; opacity: 0.95; }
}

/* 拼合入场：三片错峰 0/80/160ms，结束后摘类回到常驻撕裂 */
.assembling .s1 { animation: asmIn1 440ms var(--ease) both, slB1 var(--glitch-slice-1) steps(3, end) 460ms infinite; }
.assembling .s2 { animation: asmIn2 440ms var(--ease) 80ms both, slB2 var(--glitch-slice-2) steps(2, end) 540ms infinite; }
.assembling .s3 { animation: asmIn3 440ms var(--ease) 160ms both, slB3 var(--glitch-slice-3) steps(3, end) 620ms infinite; }

@keyframes asmIn1 {
  from { transform: translateX(-0.55em); opacity: 0; }
  to { transform: none; opacity: 0.9; }
}

@keyframes asmIn2 {
  from { transform: translateX(0.55em); opacity: 0; }
  to { transform: none; opacity: 0.85; }
}

@keyframes asmIn3 {
  from { transform: translateY(0.24em); opacity: 0; }
  to { transform: none; opacity: 0.95; }
}

/* ---------- morph 卡片 ---------- */
.card {
  position: relative;
  width: min(440px, 92vw);
  padding: 16px;
  overflow: hidden;
  border: 1px solid var(--accent);
  /* 按钮与窗口同为直角矩形：morph 期间圆角不变，矩形直接长成窗口 */
  border-radius: var(--radius);
  background-color: var(--accent);
  color: var(--accent-ink);
  cursor: pointer;
  /* 按钮尺寸：卡片恒为窗口尺寸，hero 态整体缩成一个矩形按钮 */
  transform: scale(0.4, 0.16);
  transition:
    transform var(--dur-morph) var(--ease),
    background-color var(--dur-morph) var(--ease),
    border-color var(--dur-morph) var(--ease),
    box-shadow var(--dur-morph) var(--ease);
}

.card.open {
  transform: none;
  border-color: var(--line);
  background-color: var(--surface);
  color: var(--text);
  cursor: default;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.6);
}

/* 悬停光晕：独立伪元素走微时长透明度，不与 morph 的 transition 互相干扰 */
.card::after {
  content: '';
  position: absolute;
  inset: 0;
  border-radius: inherit;
  box-shadow:
    5px 0 0 var(--glitch-r),
    -5px 0 0 var(--accent),
    0 10px 30px rgba(180, 108, 255, 0.35);
  opacity: 0;
  transition: opacity var(--dur-micro) var(--ease);
  pointer-events: none;
}

.card:not(.open):hover::after {
  opacity: 1;
}

/* 扫光 */
.card::before {
  content: '';
  position: absolute;
  inset: 0;
  pointer-events: none;
  background: linear-gradient(100deg, transparent 30%, rgba(255, 255, 255, 0.55) 50%, transparent 70%);
  transform: translateX(-120%);
}

.card:not(.open):hover::before {
  animation: sheen var(--glitch-sheen) var(--ease);
}

@keyframes sheen {
  from { transform: translateX(-120%); }
  to { transform: translateX(120%); }
}

/* 开始标签：与窗口内容共存，只切 opacity。
   反比缩放抵消卡片缩放，文字任何时刻都是原尺寸。 */
.start-label {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  font-family: var(--font-glitch);
  font-size: 22px;
  font-weight: 700;
  letter-spacing: 0.12em;
  transform: scale(2.5, 6.25);
  opacity: 1;
  pointer-events: none;
  /* 展开立即淡出；收起延迟到后半段淡入（等卡片接近按钮形态） */
  transition: opacity 160ms var(--ease) 260ms;
}

.card.open .start-label {
  opacity: 0;
  transition: opacity 160ms var(--ease);
}

/* 窗口内容：随卡片一起缩放（不做反比——反比会与卡片缓动不同步，中段鼓胀）。
   展开延后到 240ms 才淡入（卡片已近终态，压扁 ≤15%），收起立即淡出。 */
.window {
  position: relative;
  z-index: 1;
  width: 100%;
  opacity: 0;
  pointer-events: none;
  transition: opacity 200ms var(--ease);
}

.card.open .window {
  opacity: 1;
  pointer-events: auto;
  transition: opacity 200ms var(--ease) 240ms;
}

.win-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 12px;
}

.win-title {
  font-family: var(--font-glitch);
  font-size: 17px;
  letter-spacing: 0.06em;
}

.win-close {
  width: 30px;
  height: 30px;
  color: var(--dim);
  font-size: 15px;
  transition: color var(--dur-micro) var(--ease), transform var(--dur-micro) var(--ease);
}

.win-close:hover {
  color: var(--text);
}

.win-close:active {
  transform: scale(0.9);
}

/* 窗口内面板切换（登录 ↔ 直接进房）：out-in 交叉淡入，160ms */
.win-enter-active,
.win-leave-active {
  transition: opacity 160ms var(--ease), transform 160ms var(--ease);
}

.win-enter-from {
  opacity: 0;
  transform: translateY(8px);
}

.win-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}

.direct {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.back-auth {
  align-self: flex-start;
  padding: 6px 2px;
  color: var(--dim);
  font-size: 12px;
  transition: color var(--dur-micro) var(--ease);
}

.back-auth:hover {
  color: var(--text);
}

.foot {
  font-size: 12px;
  text-align: center;
}

@media (max-height: 720px) {
  .stage {
    gap: 24px;
  }

  .landing.open .hero {
    transform: translateY(-28px) scale(0.5);
  }
}
</style>
