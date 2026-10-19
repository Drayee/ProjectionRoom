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

          <!-- 滚动只发生在这里（窗口内容区），标题栏固定不动。
               窗口放得下时它没有滚动条；放不下时滚的是窗口内部，页面本身永不滚。 -->
          <div class="win-scroll">
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
      </div>

      <p class="foot dim">看片不需要账号 · 有房间码就能进</p>
    </main>
  </div>
</template>

<style scoped>
/* ---------- 布局（morph 期间无任何布局属性变化 → 滚动条永不动） ----------

   **两态都锁死一屏**：height 恒等于视口、overflow: hidden。
   为什么 hero 态也要锁：卡片布局盒恒为窗口尺寸（455px，这是 morph 不变形的
   前提），hero 态只是把它 scale(0.4, 0.16) 成按钮 —— scale 不改布局。
   于是矮屏（800×600、笔记本 125% 缩放）上布局盒 455 + 标题 132 + 脚注 19
   必然超视口，页面就长出滚动条，而**视觉上什么都没有被挡住**（按钮在视口正中）。
   那不是"内容放不下"，是"布局盒比视觉大"，用滚动条去承载它纯属噪音。

   auth 态同理，但多一步：窗口作为 flex 子项可收缩（min-height: 0），
   真放不下时由窗口内部承担。之前的写法是 min-height + 内容溢出 ——
   窗口一超高就把 body 撑出滚动条，"中间的子窗体"于是看起来像"页面的一段"。
   1280×720、1366×768 的 125% 缩放一进来就复现，不是偶发。 */
.landing {
  /* 标题字号：h1 与"缩放后应还回去多少布局高度"必须用同一个值，
     否则窄屏（clamp 落到 60px 一侧）会算错，白还几十像素。 */
  --brand-size: clamp(60px, 14vw, 132px);
  /* 标题展开态的上移量。**同一个值**同时用于 transform 与负 margin：
     transform 不参与布局，上移多少就在布局里空出多少；
     负 margin 恰好抵掉这一段，卡片才能正好贴到标题视觉底边而不重叠。
     两处写死成不同数字就会重叠（写死 -0.48em 时 800×600 上叠了 2px）。 */
  --hero-lift: 44px;
  height: 100vh;
  height: 100dvh;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  padding: 24px 20px 28px;
}

.stage {
  flex: 1;
  /* 允许子项收缩：默认 min-height: auto 会顶住内容高度，收缩永远不生效 */
  min-height: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  justify-content: safe center;
  gap: 40px;
}

/* 标题与脚注不参与收缩：要挤就挤窗口，别把标题压扁、把脚注挤没。 */
.hero,
.foot {
  flex: none;
}

/* ---------- 标题：故障黑体 + 三片撕裂（票 #13 规格，周期互质） ---------- */
.hero {
  transform-origin: 50% 100%;
  /* margin-bottom 也进 transition：auth 态要靠它把缩放后多出来的布局空间还回去，
     瞬跳会让卡片在 morph 中途弹一下。与 transform 同一条曲线，视觉上是一次连贯上移。 */
  transition:
    transform var(--dur-morph) var(--ease),
    margin-bottom var(--dur-morph) var(--ease);
  margin-bottom: 0;
}

/* 展开时缩小上移：纯 transform，不参与布局 */
.landing.open .hero {
  transform: translateY(calc(var(--hero-lift) * -1)) scale(0.52);
  /* 缩放是"视觉"的，布局盒仍是 --brand-size 高。scale 之后视觉底边比布局底边
     高出整整 --hero-lift，那一段就是白占的死区。负 margin 正好抵掉它：
     卡片于是紧贴标题视觉底边（间距由 .stage 的 gap 决定），既不重叠也不空着。
     矮屏上这几十像素就是"一条滚动条"的差额。 */
  margin-bottom: calc(var(--hero-lift) * -1);
}

.brand {
  position: relative;
  margin: 0;
  font-family: var(--font-glitch);
  font-size: var(--brand-size);
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
  /* 它是 stage 里唯一可以收缩的项：放不下时缩它，不缩标题、不缩脚注。
     min-height: 0 是必须的 —— flex 子项默认 min-height: auto 会顶住内容高度，
     写了 flex-shrink 也永远不生效（这正是"窗口撑破视口"的成因）。 */
  flex: 0 1 auto;
  min-height: 0;
  max-height: 100%;
  display: flex;
  flex-direction: column;
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
  /* 卡片是 flex 容器：窗口作为它的子项同样要允许收缩，否则内容高度会把卡片顶开 */
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.card.open .window {
  opacity: 1;
  pointer-events: auto;
  transition: opacity 200ms var(--ease) 240ms;
}

/* 窗口内容区：唯一允许滚动的层（也是最后一道兜底）。
   细滚动条 + 透明轨道：真到这一步时它看起来像窗口自带的滑轨，
   而不是"页面被挤出了滚动条"。不预留 scrollbar-gutter —— 那会让窗口
   在不需要滚动时也永久窄掉一条，等于为了不跳宽度先亏 8px。

   负 margin + 等量 padding：面板的上下装饰线是 `left:0;right:0` 再
   translateX(±5px)，两端各探出 5px。overflow 容器会把它裁掉，视觉上
   那两条错位线就"短了一截"。把裁剪框左右各撑开 6px 再抵消回来：
   面板宽度不变，装饰线完整。 */
.win-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  overscroll-behavior: contain;
  /* 左右各撑开 6px 再抵消：面板的错位装饰线两端各探出 5px，不撑开会被裁短。 */
  padding: 0 6px;
  margin: 0 -6px;
}

/* 滚动条：只写 ::-webkit-scrollbar，**不要**同时写 scrollbar-width/scrollbar-color ——
   Chrome 一旦看到标准属性就整组忽略 -webkit- 规则，退回带上下箭头的系统滚动条
   （暗色页面上像一条贴上去的白条）。这里要的是"透明轨道 + 一条细 thumb"。 */
.win-scroll::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}

.win-scroll::-webkit-scrollbar-track {
  background: transparent;
}

.win-scroll::-webkit-scrollbar-thumb {
  background: var(--line-strong);
  border: 2px solid transparent;
  background-clip: content-box;
}

.win-scroll::-webkit-scrollbar-thumb:hover {
  background: var(--accent);
  background-clip: content-box;
}

/* 不支持 -webkit- 滚动条的环境（Firefox）：退回标准属性，至少能变细、能变暗 */
@supports not selector(::-webkit-scrollbar) {
  .win-scroll {
    scrollbar-width: thin;
    scrollbar-color: var(--line-strong) transparent;
  }
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
  /* <p> 的 UA 默认 margin-block 是 1em（这里上下各 12px，合计 24px 的隐形占位）。
     它不显眼，但矮屏上"差一条滚动条"的差额往往正是它。 */
  margin: 0;
}

@media (max-height: 720px) {
  .stage {
    gap: 24px;
  }

  /* 矮屏：上移量减半（给窗口腾地方），scale 也再小一点。
     两个值一起改 —— 负 margin 跟着 --hero-lift 走，不会与 transform 脱钩。 */
  .landing {
    --hero-lift: 28px;
  }

  .landing.open .hero {
    transform: translateY(calc(var(--hero-lift) * -1)) scale(0.5);
  }
}
</style>
