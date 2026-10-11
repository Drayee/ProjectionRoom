<script setup lang="ts">
/**
 * 首页：一个路由两种形态（授权 100% 在服务端，这里只按登录态换渲染内容）。
 *   - 未登录：落地页（月喵 + 开始）→ 点「开始」morph 成半透明登录窗；
 *   - 已登录：外壳（icon-first 标题栏 + FAB + 建房/进房/我的房间）。
 *
 * 落地页视觉清理（按 YG 反馈）：
 *   - 去掉 "一起看 · 作者 drayee · 开源" 副标题
 *   - 去掉 HeroMotion 背景光晕噪点
 *   - > 改为竖线 | 上下浮动
 *   - 加强 morph 动画丝滑度
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import AuthPanel from '../components/AuthPanel.vue'
import BrandIcon from '../components/BrandIcon.vue'
import CreateRoomCard from '../components/CreateRoomCard.vue'
import HomeFab from '../components/HomeFab.vue'
import JoinRoomCard from '../components/JoinRoomCard.vue'
import MyRoomsCard from '../components/MyRoomsCard.vue'
import { useAuthStore } from '../stores/auth'
import { cssVars } from '../utils/cssVars'

const auth = useAuthStore()
const REPO_URL = 'https://github.com/Drayee/ProjectionRoom'
const NAME_KEY = 'pr:name'

const displayName = ref(
  localStorage.getItem(NAME_KEY) ?? auth.profile?.displayName ?? `观众${Math.floor(Math.random() * 900 + 100)}`,
)

// ---------- 落地页：hero（标题 + 开始）↔ auth（登录窗）----------
type Stage = 'hero' | 'auth'
const stage = ref<Stage>('hero')
const transitioning = ref(false)
const expiredHint = ref('')
const windowContent = ref(false)
let transitionTimer: ReturnType<typeof setTimeout> | undefined

function finishTransition() {
  transitioning.value = false
  if (stage.value === 'hero') windowContent.value = false
}

function setStage(next: Stage) {
  stage.value = next
  transitioning.value = true
  if (next === 'auth') windowContent.value = true
  if (transitionTimer !== undefined) clearTimeout(transitionTimer)
  transitionTimer = setTimeout(finishTransition, 760)
}

function toggleStage() {
  setStage(stage.value === 'hero' ? 'auth' : 'hero')
}

function onWindowTransitionEnd(event: TransitionEvent) {
  if (event.target !== authWindow.value) return
  if (event.propertyName !== 'transform' && event.propertyName !== 'opacity') return
  finishTransition()
}

const authWindow = ref<HTMLElement | null>(null)
const windowHeight = ref(0)
let windowObserver: ResizeObserver | undefined
const morphStyle = computed(() => cssVars({ '--aw-h': `${windowHeight.value}px` }))

function measured() {
  windowHeight.value = authWindow.value?.offsetHeight ?? 0
}

watch(
  () => [stage.value, windowContent.value] as const,
  async () => {
    await nextTick()
    requestAnimationFrame(measured)
  },
)

onMounted(() => {
  measured()
  if (typeof ResizeObserver !== 'undefined' && authWindow.value) {
    windowObserver = new ResizeObserver(measured)
    windowObserver.observe(authWindow.value)
  }
  void bootstrapAuth()
})

onBeforeUnmount(() => {
  if (transitionTimer !== undefined) clearTimeout(transitionTimer)
  windowObserver?.disconnect()
  windowObserver = undefined
})

async function bootstrapAuth() {
  const hadCredential = hasStoredCredential()
  await auth.bootstrap()
  if (!auth.isLoggedIn && hadCredential) {
    expiredHint.value = '登录状态已失效：请重新登录，或者直接进入放映室看片（看片不需要账号）。'
    setStage('auth')
  }
}

function hasStoredCredential(): boolean {
  try {
    return sessionStorage.getItem('pr:access') !== null || localStorage.getItem('pr:profile') !== null
  } catch {
    return false
  }
}

// ---------- 已登录：外壳 ----------
interface CreateCardHandle {
  createRoom: () => Promise<void>
  $el?: Element
}
const createCard = ref<CreateCardHandle | null>(null)
const capabilityNotice = ref('')

function onCreateRoom() {
  const card = createCard.value
  if (!card) return
  card.$el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  void card.createRoom()
}

function onCapability(kind: 'post' | 'anime') {
  capabilityNotice.value =
    kind === 'post'
      ? '发送帖子：能力待接入。后端接口与数据模型（posts）尚未实现，本轮只在快捷入口里保留位置。'
      : '添加番剧：能力待接入。番剧目录与数据模型尚未实现，本轮只在快捷入口里保留位置。'
}

const booting = computed(() => auth.restoring)
</script>

<template>
  <Transition name="home" mode="out-in">
    <!-- 恢复登录中 -->
    <div v-if="booting" key="boot" class="home boot" data-testid="home-restoring">
      <p class="muted">恢复登录中…</p>
    </div>

    <!-- 已登录：外壳 -->
    <div v-else-if="auth.isLoggedIn" key="shell" class="home shell" data-testid="home-shell">
      <AppHeader variant="shell" />
      <HomeFab @create="onCreateRoom" @post="onCapability('post')" @anime="onCapability('anime')" />

      <div v-if="capabilityNotice" class="capability" data-testid="home-capability-notice">
        <BrandIcon name="tips" label="提示" :size="15" />
        <span class="msg">{{ capabilityNotice }}</span>
        <button type="button" class="icon-btn" aria-label="关闭提示" v-tip="'关闭提示'" data-testid="home-capability-close" @click="capabilityNotice = ''">
          <BrandIcon name="close" decorative :size="14" />
        </button>
      </div>

      <div class="grid">
        <CreateRoomCard ref="createCard" v-model:name="displayName" />
        <JoinRoomCard v-model:name="displayName" :show-name="false" heading="加入房间" />
      </div>
      <MyRoomsCard />
    </div>

    <!-- 未登录：落地页 -->
    <div v-else key="landing" class="home landing" data-testid="home-landing" :data-stage="stage" :data-transitioning="transitioning ? 'true' : 'false'" :class="{ 'is-auth': stage === 'auth' }">
      <!-- 右上角 GitHub / 作者 / 帮助 -->
      <div class="landing-top">
        <a class="round-btn" :href="REPO_URL" target="_blank" rel="noopener noreferrer" aria-label="开源仓库（作者 drayee）" v-tip="'开源仓库（作者 drayee）'" data-testid="home-github">
          <BrandIcon name="github" decorative :size="18" />
        </a>
        <RouterLink class="round-btn" :to="{ name: 'about' }" aria-label="关于作者" v-tip="'关于作者'" data-testid="home-about">
          <BrandIcon name="star" decorative :size="18" />
        </RouterLink>
        <RouterLink class="round-btn" :to="{ name: 'help' }" aria-label="帮助与说明" v-tip="'帮助与说明'" data-testid="home-help">
          <BrandIcon name="tips" decorative :size="18" />
        </RouterLink>
      </div>

      <div class="landing-stage">
        <header class="hero-head" :class="{ lifted: stage === 'auth' }">
          <h1 class="moon-title" data-testid="home-title">月喵</h1>
        </header>

        <div class="morph" :class="{ open: stage === 'auth', 'is-transitioning': transitioning }" :style="morphStyle">
          <button type="button" class="start" :class="{ 'is-open': stage === 'auth' }" :aria-expanded="stage === 'auth'" aria-controls="home-auth-window" data-testid="home-start" @click="toggleStage">
            <span class="start-label" data-testid="home-start-label">开始</span>
          </button>

          <section id="home-auth-window" ref="authWindow" class="auth-window" data-testid="home-auth-window" :aria-hidden="stage !== 'auth'" @transitionend="onWindowTransitionEnd">
            <template v-if="windowContent">
              <div class="window-bar">
                <span class="window-title"><BrandIcon name="login" decorative :size="15" /> 登录月喵</span>
                <button type="button" class="round-btn small" aria-label="返回（收起登录窗口）" v-tip="'返回（收起登录窗口）'" data-testid="home-auth-close" @click="toggleStage">
                  <BrandIcon name="close" decorative :size="16" />
                </button>
              </div>
              <p v-if="expiredHint" class="window-hint" data-testid="home-auth-expired">
                <BrandIcon name="alert-triangle" label="注意" :size="14" />
                {{ expiredHint }}
              </p>
              <AuthPanel plain direct-entry>
                <template #direct>
                  <JoinRoomCard v-model:name="displayName" heading="直接进入放映室" direct data-testid="home-direct-join" />
                </template>
              </AuthPanel>
            </template>
          </section>
        </div>

        <!-- 竖线 | 上下浮动 -->
        <span class="chev" aria-hidden="true" data-testid="home-chevron">|</span>
      </div>

      <p class="landing-foot muted small">
        <RouterLink :to="{ name: 'public-rooms' }" class="text-link" data-testid="home-public-rooms">
          <BrandIcon name="video" decorative :size="13" />
          看看公开的房间
        </RouterLink>
        <span>· 看片不需要账号，有房间码就能进</span>
      </p>
    </div>
  </Transition>
</template>

<style scoped>
.home {
  max-width: 980px;
  margin: 0 auto;
  padding: 36px 20px 44px;
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.home.boot { min-height: 40vh; justify-content: center; align-items: center; }

/* ---------------- 落地页 ---------------- */
.home.landing { min-height: 100vh; padding: 18px 20px 28px; gap: 14px; }

.landing-top { display: flex; align-items: center; justify-content: flex-end; gap: 8px; }

.round-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 36px; height: 36px; padding: 0; border-radius: 50%;
  border: 1px solid var(--border);
  background: rgba(31, 31, 39, 0.6);
  color: var(--text-dim); text-decoration: none; line-height: 0;
}
.round-btn:hover { color: var(--text); border-color: var(--accent); text-decoration: none; }
.round-btn.small { width: 30px; height: 30px; }

.landing-stage {
  flex: 1; display: flex; flex-direction: column; align-items: center;
  justify-content: center; gap: 24px;
}

.hero-head {
  display: flex; flex-direction: column; align-items: center; gap: 0;
  transition: transform 0.6s var(--ease-out), opacity 0.6s ease;
}
.hero-head.lifted {
  transform: translateY(-clamp(48px, 18vh, 190px)) scale(0.28);
  opacity: 0.10;
}

.moon-title {
  margin: 0;
  font-size: clamp(60px, 14vw, 120px);
  line-height: 1.05;
  letter-spacing: 0.06em;
  color: var(--text);
  z-index: 1;
}

/* 变形舞台 */
.morph {
  position: relative; display: flex; justify-content: center;
  width: 100%; min-height: 56px;
  transition: min-height 0.6s var(--ease-out);
}
.morph.open { min-height: var(--aw-h, 440px); }

/* 开始按钮 */
button.start {
  align-self: flex-start; display: inline-flex; align-items: center; justify-content: center;
  min-width: 168px; padding: 16px 52px;
  border-radius: var(--radius-pill);
  background: var(--accent); border: 1px solid var(--accent);
  color: var(--accent-ink); font-size: 20px; font-weight: 700; letter-spacing: 0.16em;
  transition: opacity 0.5s ease 0.3s, transform 0.35s ease;
  box-shadow: 0 4px 20px rgba(0, 161, 214, 0.35);
}
button.start:hover { transform: translateY(-2px); box-shadow: 0 6px 28px rgba(0, 161, 214, 0.45); }
button.start.is-open { opacity: 0; }

.morph.is-transitioning button.start { z-index: 3; }
.morph.is-transitioning .auth-window { pointer-events: none; }

.start-label { transition: opacity 0.25s ease; }
button.start.is-open .start-label { opacity: 0; }

/* 登录窗 */
.auth-window {
  position: absolute; top: 0; left: 50%;
  width: min(440px, 100%); padding: 16px;
  border-radius: var(--radius);
  border: 1px solid var(--border);
  background: color-mix(in srgb, var(--panel-solid) 72%, transparent);
  backdrop-filter: blur(var(--glass-blur));
  -webkit-backdrop-filter: blur(var(--glass-blur));
  box-shadow: var(--shadow);
  transform-origin: top center;
  transform: translateX(-50%) scale(0.35);
  opacity: 0; visibility: hidden; pointer-events: none;
  transition: transform 0.6s var(--ease-out), opacity 0.35s ease, border-radius 0.6s ease, visibility 0s linear 0.6s;
}
.morph.open .auth-window {
  transform: translateX(-50%) scale(1);
  opacity: 0.96; visibility: visible; pointer-events: auto;
  transition: transform 0.6s var(--ease-out), opacity 0.4s ease 0.15s, border-radius 0.6s ease, visibility 0s linear 0s;
}
.morph:not(.open) .auth-window { border-radius: var(--radius-pill); }

.window-bar {
  display: flex; align-items: center; justify-content: space-between;
  gap: 10px; margin-bottom: 12px;
}
.window-title { display: inline-flex; align-items: center; gap: 6px; font-size: 15px; font-weight: 600; }
.window-hint {
  display: flex; align-items: flex-start; gap: 6px;
  margin: 0 0 12px; padding: 8px 10px;
  border-radius: var(--radius-sm); border: 1px solid var(--border);
  background: var(--panel-2); color: var(--text-dim); font-size: 12px; line-height: 1.6;
}

/* 竖线 | 上下浮动 */
.chev {
  font-size: 24px; line-height: 1; color: var(--accent);
  animation: chev-float 1.8s ease-in-out infinite;
  opacity: 0.6;
}
.home.landing.is-auth .chev { opacity: 0; animation: none; }

@keyframes chev-float {
  0%   { transform: translateY(-10px); opacity: 0.3; }
  50%  { transform: translateY(6px);  opacity: 0.7; }
  100% { transform: translateY(-10px); opacity: 0.3; }
}

.home.landing.is-auth :deep(.hero-motion) { opacity: 0.3; }

.landing-foot {
  display: flex; align-items: center; justify-content: center;
  gap: 10px; flex-wrap: wrap; margin: 0; text-align: center;
}
.text-link { display: inline-flex; align-items: center; gap: 4px; text-decoration: none; }

/* ---------------- 外壳 ---------------- */
.capability {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 12px; border: 1px solid var(--border);
  border-radius: var(--radius-sm); background: var(--panel-2);
  color: var(--text-dim); font-size: 12px; line-height: 1.6;
}
.capability .msg { flex: 1; min-width: 0; }

button.icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 3px; background: transparent; border-color: transparent;
  color: inherit; line-height: 0;
}

.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; align-items: start; }
@media (max-width: 760px) { .grid { grid-template-columns: minmax(0, 1fr); } }
@media (max-width: 460px) {
  .home { padding: 20px 12px 32px; }
  .home.landing { padding: 14px 12px 22px; }
  .auth-window { padding: 12px; }
}

/* landing ↔ shell 整屏淡入淡出 */
.home-enter-active, .home-leave-active { transition: opacity 0.3s ease; }
.home-enter-from, .home-leave-to { opacity: 0; }

@media (prefers-reduced-motion: reduce) {
  .hero-head, .morph, .auth-window, button.start, .start-label { transition: none; }
  .chev { animation: none; }
  .home-enter-active, .home-leave-active { transition: none; }
}
</style>
