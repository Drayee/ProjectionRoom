<script setup lang="ts">
/**
 * 落地页外壳（票 #10 B 定稿 · 票 #13 动效规格）。
 *
 * 三态：hero（月喵标题 + 开始按钮）↔ auth（登录窗）。
 * /login、/register 复用本组件：initialStage='auth' + 对应页签，
 * 不再是"另一张表单页"（YG 裁决：注册没有单独界面）。
 *
 * 动效硬约束（票 #13）：
 *   - 撕裂三片周期 2600/3100/3700ms **互质**，steps() 阶梯；
 *   - 抖动幅度按 em（原型 9–18px @ 132px 字号 ≈ .07–.14em）；
 *   - 拼合入场 460ms 错开 0/80/160ms，播完**摘类**；
 *   - morph 440ms：按钮→登录窗走 CSS transition（min-width/padding/border-radius），
 *     不再用 WAAPI FLIP——FLIP 的 transform keyframe 会覆盖居中 transform 且与
 *     CSS transition 双重叠加，曾导致「突然变圆 / 窗口右移」。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import AuthPanel from '../auth/AuthPanel.vue'
import JoinRoomCard from '../room/JoinRoomCard.vue'
import type { AuthTab } from '../auth/useAuthForm'

const props = withDefaults(
  defineProps<{
    /** 初始形态：落地页 / 登录窗展开。 */
    initialStage?: 'hero' | 'auth'
    /** 登录窗初始页签。 */
    initialTab?: AuthTab
    /** 是否显示「直接进放映室」入口（落地页形态要）。 */
    directEntry?: boolean
  }>(),
  { initialStage: 'hero', initialTab: 'login', directEntry: true },
)

const stage = ref<'hero' | 'auth'>(props.initialStage)
const showDirect = ref(false)
const transitioning = ref(false)
const assembling = ref(true)
let transitionTimer: ReturnType<typeof setTimeout> | undefined
let assembleTimer: ReturnType<typeof setTimeout> | undefined

const displayName = ref(
  (() => {
    try {
      return localStorage.getItem('pr:name') || ''
    } catch {
      return ''
    }
  })(),
)

function finishTransition() {
  transitioning.value = false
}

function setStage(next: 'hero' | 'auth') {
  if (stage.value === next) return
  stage.value = next
  transitioning.value = true
  if (transitionTimer !== undefined) clearTimeout(transitionTimer)
  transitionTimer = setTimeout(finishTransition, 800)
}

function startClicked() {
  setStage('auth')
}

function collapse() {
  setStage('hero')
}

onMounted(() => {
  // 拼合入场播完摘类（460 + 160 延迟 + 余量）
  assembleTimer = setTimeout(() => (assembling.value = false), 1100)
})

onBeforeUnmount(() => {
  if (transitionTimer !== undefined) clearTimeout(transitionTimer)
  if (assembleTimer !== undefined) clearTimeout(assembleTimer)
})
</script>

<template>
  <div class="landing" :class="{ 'is-auth': stage === 'auth' }">
    <div class="stage-area">
      <!-- 标题：故障黑体 + 三片撕裂（纯装饰切片 aria-hidden，票 #13 §6.1） -->
      <header class="hero-head" :class="{ lifted: stage === 'auth', assembling }">
        <h1 class="brand">
          <span class="sr">月喵</span>
          <span class="sl sl1" aria-hidden="true">月喵</span>
          <span class="sl sl2" aria-hidden="true">月喵</span>
          <span class="sl sl3" aria-hidden="true">月喵</span>
        </h1>
      </header>

      <!-- 开始按钮 → 登录窗 morph 容器 -->
      <div class="morph-box">
        <div
          class="morph-target"
          :class="{ open: stage === 'auth' }"
          :role="stage === 'hero' ? 'button' : undefined"
          :tabindex="stage === 'hero' ? 0 : -1"
          :aria-label="stage === 'hero' ? '开始' : undefined"
          @click="stage === 'hero' && startClicked()"
          @keydown.enter.prevent="stage === 'hero' && startClicked()"
          @keydown.space.prevent="stage === 'hero' && startClicked()"
        >
          <span v-if="stage === 'hero' || transitioning" class="start-label">开始</span>

          <div v-if="stage === 'auth' || transitioning" class="auth-inner">
            <div class="window-bar">
              <span class="window-title">登录月喵</span>
              <button
                v-if="props.initialStage !== 'auth'"
                type="button"
                class="collapse"
                aria-label="收起登录窗"
                v-tip="'收起'"
                @click.stop="collapse"
              >
                ✕
              </button>
            </div>
            <AuthPanel
              v-if="!showDirect"
              plain
              :initial-tab="props.initialTab"
              :direct-entry="props.directEntry"
              @direct="showDirect = true"
            />
            <template v-else>
              <JoinRoomCard :name="() => displayName || '观众'" />
              <button class="back-auth" type="button" @click="showDirect = false">
                ← 返回登录
              </button>
            </template>
          </div>
        </div>
      </div>

      <p class="foot dim">看片不需要账号 · 有房间码就能进</p>
    </div>
  </div>
</template>

<style scoped>
.landing {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  padding: 18px 20px 28px;
}

.stage-area {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 28px;
}

/* ---------- 标题：三片撕裂 ---------- */
.hero-head {
  display: flex;
  flex-direction: column;
  align-items: center;
  transition: transform var(--dur-morph) var(--ease), opacity 500ms var(--ease);
}

.hero-head.lifted {
  transform: translateY(-clamp(40px, 14vh, 160px)) scale(0.4);
  opacity: 0;
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

.brand .sr {
  visibility: hidden;
}

.brand .sl {
  position: absolute;
  left: 0;
  top: 0;
  width: 100%;
  visibility: visible;
}

.brand .sl1 {
  clip-path: inset(0 0 66% 0);
  color: var(--accent);
  animation: slB1 var(--glitch-slice-1) steps(3, end) infinite;
}

.brand .sl2 {
  clip-path: inset(33% 0 33% 0);
  color: var(--glitch-p);
  animation: slB2 var(--glitch-slice-2) steps(2, end) infinite;
}

.brand .sl3 {
  clip-path: inset(66% 0 0 0);
  color: var(--glitch-r);
  animation: slB3 var(--glitch-slice-3) steps(3, end) infinite;
}

/* 抖动幅度按 em（票 #13：原型 9–18px @ 132px ≈ .07–.14em） */
@keyframes slB1 {
  0%, 80% { transform: none; opacity: 0.9 }
  84% { transform: translateX(-0.14em); opacity: 1 }
  88%, 100% { transform: none; opacity: 0.9 }
}

@keyframes slB2 {
  0%, 86% { transform: none; opacity: 0.85 }
  90% { transform: translateX(0.11em) }
  94%, 100% { transform: none; opacity: 0.85 }
}

@keyframes slB3 {
  0%, 90% { transform: none; opacity: 0.95 }
  93% { transform: translateX(-0.07em) skewX(-6deg) }
  96%, 100% { transform: none; opacity: 0.95 }
}

/* 拼合入场：三片从不同方向飞入，错开 0/80/160ms；播完父级摘类 */
.assembling .sl1 {
  animation: asmIn1 var(--dur-morph) var(--ease) both, slB1 var(--glitch-slice-1) steps(3, end) 460ms infinite;
}

.assembling .sl2 {
  animation: asmIn2 var(--dur-morph) var(--ease) 80ms both, slB2 var(--glitch-slice-2) steps(2, end) 540ms infinite;
}

.assembling .sl3 {
  animation: asmIn3 var(--dur-morph) var(--ease) 160ms both, slB3 var(--glitch-slice-3) steps(3, end) 620ms infinite;
}

@keyframes asmIn1 {
  from { transform: translateX(-0.55em); opacity: 0 }
  to { transform: none; opacity: 0.9 }
}

@keyframes asmIn2 {
  from { transform: translateX(0.55em); opacity: 0 }
  to { transform: none; opacity: 0.85 }
}

@keyframes asmIn3 {
  from { transform: translateY(0.24em); opacity: 0 }
  to { transform: none; opacity: 0.95 }
}

/* ---------- morph：按钮 → 登录窗（纯 CSS transition，440ms） ---------- */
.morph-box {
  display: flex;
  justify-content: center;
  width: 100%;
}

/**
 * morph 容器：按钮态与登录窗态在过渡中会**同时挂载**（`transitioning` 窗口内），
 * 所以子节点必须**叠在同一格**里（`grid-row/column: 1`）。
 *
 * ⚠️ 只写 `display:grid` 不够：默认 `grid-auto-flow: row` 会让两个子节点各占一行，
 * 容器尺寸被两者的组合撑大（实测过渡首帧宽 491px、`transitioning` 关闭后跳回 440px），
 * 表现为「窗口在右侧生成 + 内容突然右移后又正常」。叠格后容器宽高只由窗口本身决定。
 */
.morph-target {
  position: relative; /* ::before 掠过亮带的定位上下文（no absolute 居中，改 flex 居中） */
  display: grid;
  grid-template-columns: 1fr;
  align-items: center;
  justify-items: center;
  min-width: 168px;
  padding: 16px 52px;
  border-radius: var(--radius-pill);
  border: 1px solid var(--accent);
  background: var(--accent);
  color: var(--accent-ink);
  font-size: 20px;
  font-weight: 700;
  letter-spacing: 0.16em;
  cursor: pointer;
  overflow: hidden;
  transition:
    min-width var(--dur-morph) var(--ease),
    padding var(--dur-morph) var(--ease),
    border-radius var(--dur-morph) var(--ease),
    border-color var(--dur-morph) var(--ease),
    background var(--dur-morph) var(--ease),
    color var(--dur-morph) var(--ease),
    font-size var(--dur-morph) var(--ease),
    letter-spacing var(--dur-morph) var(--ease),
    font-weight var(--dur-morph) var(--ease);
}

/* 标签与登录窗叠在同一格：重叠而非并排/堆叠，容器尺寸只取窗口 */
.morph-target > * {
  grid-row: 1;
  grid-column: 1;
}

/* 悬停：RGB 分离外发光 + 字距拉开（票 #13 效果 8） */
.morph-target:not(.open):hover {
  background: var(--accent-hover);
  letter-spacing: 0.2em;
  box-shadow:
    5px 0 0 var(--glitch-r),
    -5px 0 0 var(--accent),
    0 10px 30px rgba(180, 108, 255, 0.35);
}

.morph-target:not(.open):active {
  transform: scale(0.97);
}

/* 掠过亮带（票 #13 效果 9） */
.morph-target:not(.open)::before {
  content: '';
  position: absolute;
  inset: 0;
  pointer-events: none;
  background: linear-gradient(100deg, transparent 30%, rgba(255, 255, 255, 0.55) 50%, transparent 70%);
  transform: translateX(-120%);
}

.morph-target:not(.open):hover::before {
  animation: sheen var(--glitch-sheen) var(--ease);
}

@keyframes sheen {
  from { transform: translateX(-120%) }
  to { transform: translateX(120%) }
}

/* auth 阶段：放大为登录窗（直角，B 语言） */
.morph-target.open {
  min-width: min(440px, 86vw);
  padding: 16px;
  border-radius: var(--radius);
  border-color: var(--line);
  background: var(--surface);
  color: var(--text);
  font-size: 14px;
  font-weight: 400;
  letter-spacing: 0;
  cursor: default;
}

.start-label {
  transition: opacity 250ms var(--ease);
  pointer-events: none;
}

.morph-target.open .start-label {
  opacity: 0;
}

.auth-inner {
  width: 100%;
  animation: fade-in 350ms var(--ease) 180ms both;
}

@keyframes fade-in {
  from { opacity: 0; transform: translateY(4px) }
  to { opacity: 1; transform: none }
}

.window-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 12px;
}

.window-title {
  font-family: var(--font-glitch);
  font-size: 17px;
  letter-spacing: 0.06em;
}

.collapse {
  width: 30px;
  height: 30px;
  color: var(--dim);
  font-size: 15px;
  transition: color var(--dur-micro) var(--ease), transform var(--dur-micro) var(--ease);
}

.collapse:hover {
  color: var(--text);
}

.collapse:active {
  transform: scale(0.9);
}

.foot {
  font-size: 12px;
  text-align: center;
}

.back-auth {
  margin-top: 10px;
  align-self: flex-start;
  padding: 6px 2px;
  color: var(--dim);
  font-size: 12px;
  transition: color var(--dur-micro) var(--ease);
}

.back-auth:hover {
  color: var(--text);
}

/* ---------- 降级：动画判死（含伪元素） ---------- */
@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation: none !important;
    transition: none !important;
  }
}
</style>
