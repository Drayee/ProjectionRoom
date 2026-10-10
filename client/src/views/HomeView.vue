<script setup lang="ts">
/**
 * 首页：**一个路由两种形态**。
 *
 *   - 未登录：先是一屏落地页（大标题「月喵」+「开始」+ 标题背后的动图槽位 + 抖动的小 `>`），
 *     点「开始」后**同一个元素**放大变形为半透明的登录窗口（标题缩小上移）。
 *     登录窗口里是登录/注册/忘记密码三个页签 + 「直接进入放映室」（免登录进房，
 *     与 `JoinRoomCard` 同一条逻辑，见 AuthPanel 的 `#direct` 插槽）；
 *   - 已登录：标题上移成**标题栏**（分区 + 头像），下面是圆形加号 FAB 与核心功能卡片。
 *
 * 为什么不做成两个路由 + 守卫：授权 100% 在服务端（规格 §11），守位只能控"看到什么"，
 * 而观众进房本来就不需要登录，一个"未登录一律跳登录页"的守位会顺手把那扇门关上。
 * 这里只按**当前登录态**换渲染内容，`/login`、`/register` 仍然保留（回跳与深链依赖它们）。
 *
 * 鉴权（要求 A6）：进根域名**先验鉴权**，而且**复用**既有的 `auth.bootstrap()`
 *（main.ts 已经调过一次，这里是等它出结果 —— 它是内部幂等的单例 Promise）。
 * 有凭证但鉴权失败（401 且刷新也不成，bootstrap 内部已把本机凭证清干净）→ 直接落到
 * 登录窗口并说明原因，而不是给一屏空白。
 *
 * 长说明（为什么建房要登录、怎么准备片源、房间码/密码是什么、卡顿怎么办、诊断指标、
 * 隐私与安全、常见问题）全部搬到 `/help`，本页每个入口只留一句话 + 指向它的图标链接。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import AuthPanel from '../components/AuthPanel.vue'
import BrandIcon from '../components/BrandIcon.vue'
import CreateRoomCard from '../components/CreateRoomCard.vue'
import HeroMotion from '../components/HeroMotion.vue'
import HomeFab from '../components/HomeFab.vue'
import JoinRoomCard from '../components/JoinRoomCard.vue'
import MyRoomsCard from '../components/MyRoomsCard.vue'
import { useAuthStore } from '../stores/auth'
import { cssVars } from '../utils/cssVars'

const auth = useAuthStore()

/** 开源仓库地址（作者 drayee）。 */
const REPO_URL = 'https://github.com/Drayee/ProjectionRoom'

const NAME_KEY = 'pr:name'

/**
 * 本机昵称。优先级：本机上次用过的 > **账号昵称**（登录了就不用再想办法起名字）> 随机。
 * 从登录页回跳到首页时组件会重新创建，所以用账号昵称兜底这件事在"登录后回到发起处"时也成立。
 *
 * 建房与进房共用这一份（通过 `v-model:name`），不再各存一份。
 */
const displayName = ref(
  localStorage.getItem(NAME_KEY) ?? auth.profile?.displayName ?? `观众${Math.floor(Math.random() * 900 + 100)}`,
)

// ---------- 落地页（未登录）的两种阶段 ----------

/** hero = 标题 + 开始；auth = 登录窗口。 */
type Stage = 'hero' | 'auth'
const stage = ref<Stage>('hero')

/**
 * 是否正在过渡。
 *
 * 它**不**用来"屏蔽点击"（那会让过渡不可打断）—— 恰恰相反：
 *   - 过渡期间「开始」保持可点、且 `z-index` 压在登录窗口之上，所以连点会立刻反向；
 *   - 动画本身是 CSS transition（可重定向），连点不会叠出第二条动画，只是原地折返。
 * 它只用来驱动两个真实状态：`data-transitioning`（可测）与"过渡期间不让窗口吃事件"。
 */
const transitioning = ref(false)
let transitionTimer: ReturnType<typeof setTimeout> | undefined

/** 进入登录窗口的原因（凭证失效时要说清，不能只是"突然弹出登录框"）。 */
const expiredHint = ref('')

/**
 * 窗口内容是否挂载。
 *
 * 打开时立刻挂载；**关闭时挂到过渡结束再卸载** —— 否则窗口在缩小过程中会突然变空，
 * 看起来像"窗口碎了"而不是"窗口收回了按钮里"。
 */
const windowContent = ref(false)

/** 过渡收尾（transitionend 与兜底计时器共用一份，避免两条路各自清一半）。 */
function finishTransition() {
  transitioning.value = false
  if (stage.value === 'hero') windowContent.value = false
}

function setStage(next: Stage) {
  stage.value = next
  transitioning.value = true
  if (next === 'auth') windowContent.value = true
  if (transitionTimer !== undefined) clearTimeout(transitionTimer)
  // 过渡结束由 transitionend 收尾；这是**兜底**（窗口被遮挡、系统降级动画等场景下事件可能不来）。
  transitionTimer = setTimeout(finishTransition, 760)
}

function toggleStage() {
  setStage(stage.value === 'hero' ? 'auth' : 'hero')
}

/** 窗口自身的过渡结束：只接自己、只接我们关心的两个属性，避免被子元素的过渡冒泡误清。 */
function onWindowTransitionEnd(event: TransitionEvent) {
  if (event.target !== authWindow.value) return
  if (event.propertyName !== 'transform' && event.propertyName !== 'opacity') return
  finishTransition()
}

const authWindow = ref<HTMLElement | null>(null)
/** 窗口实测高度（px）：写进 `--aw-h`，让容器的高度跟着窗口一起长，过渡期间布局不乱跳。 */
const windowHeight = ref(0)
let windowObserver: ResizeObserver | undefined

const morphStyle = computed(() => cssVars({ '--aw-h': `${windowHeight.value}px` }))

const measured = (): void => {
  windowHeight.value = authWindow.value?.offsetHeight ?? 0
}

/**
 * 内容挂载/卸载之后重新量一次高度。
 *
 * ResizeObserver 通常也会在下一帧补上，但它是异步的：高度只在这两种时刻变化时
 * 主动量一次，`--aw-h` 才与"这一帧看到的内容"对齐（不会先按旧高度撑一下再跳）。
 */
watch(
  () => [stage.value, windowContent.value] as const,
  async () => {
    await nextTick()
    // 等一帧：字体/输入框的固有尺寸要在样式生效后才量得准。
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

/**
 * 进根域名先验鉴权。
 *
 * `hadCredential` 必须在 `bootstrap()` **之前**取：bootstrap 失败时会顺手清掉本机凭证
 * （那是它的既定行为），失败之后再读就只能看到"什么都没有"，区分不出
 * "本来就没登录"与"登录状态失效了"。
 */
async function bootstrapAuth() {
  const hadCredential = hasStoredCredential()
  await auth.bootstrap()
  // 有凭证却没换来登录态 = 401 且刷新也不成：落到登录窗口（而不是空白页）并说明原因。
  if (!auth.isLoggedIn && hadCredential) {
    expiredHint.value = '登录状态已失效：请重新登录，或者直接进入放映室看片（看片不需要账号）。'
    setStage('auth')
  }
}

function hasStoredCredential(): boolean {
  try {
    return sessionStorage.getItem('pr:access') !== null || localStorage.getItem('pr:profile') !== null
  } catch {
    // 隐私模式下存储可能直接抛错：当作"没有凭证"，bootstrap 自己会安静地匿名。
    return false
  }
}

// ---------- 已登录：外壳 ----------

/** 建房卡片的句柄：FAB 的「新建房间」走的就是它内部那份既有逻辑，不另写一套。 */
interface CreateCardHandle {
  createRoom: () => Promise<void>
  $el?: Element
}
const createCard = ref<CreateCardHandle | null>(null)

/** FAB ②③ 的中性提示（能力待接入）——既不是成功也不是失败，所以不复用 auth.notice 的红色。 */
const capabilityNotice = ref('')

function onCreateRoom() {
  const card = createCard.value
  if (!card) return
  // 先把卡片带到眼前再触发：失败时（未登录、限速、昵称空）错误文案就在这一屏里，
  // 不会出现"点了按钮什么都没发生"。
  card.$el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  void card.createRoom()
}

function onCapability(kind: 'post' | 'anime') {
  capabilityNotice.value =
    kind === 'post'
      ? '发送帖子：能力待接入。后端接口与数据模型（posts）尚未实现，本轮只在快捷入口里保留位置，不会创建任何内容。'
      : '添加番剧：能力待接入。番剧目录与数据模型尚未实现，本轮只在快捷入口里保留位置，不会写入任何数据。'
}

/** 未登录时先看到"正在恢复"，避免把有会话的用户先闪成落地页。 */
const booting = computed(() => auth.restoring)
</script>

<template>
  <!-- 恢复登录中：只在"有凭证、等一次 refresh"的首屏出现（一个请求的时长）。 -->
  <div class="home boot" v-if="booting" data-testid="home-restoring">
    <p class="muted">恢复登录中…</p>
  </div>

  <!-- ============ 已登录：标题栏 + 分区 + FAB + 核心卡片 ============ -->
  <div class="home shell" v-else-if="auth.isLoggedIn" data-testid="home-shell">
    <AppHeader variant="shell" />

    <HomeFab @create="onCreateRoom" @post="onCapability('post')" @anime="onCapability('anime')" />

    <!-- FAB ②③ 的中性提示：说清"能力待接入"而不是假装成功。 -->
    <div class="capability" v-if="capabilityNotice" data-testid="home-capability-notice">
      <BrandIcon name="tips" label="提示" :size="15" />
      <span class="msg">{{ capabilityNotice }}</span>
      <button
        type="button"
        class="icon-btn"
        aria-label="关闭提示"
        v-tip="'关闭提示'"
        data-testid="home-capability-close"
        @click="capabilityNotice = ''"
      >
        <BrandIcon name="close" decorative :size="14" />
      </button>
    </div>

    <div class="grid">
      <CreateRoomCard ref="createCard" v-model:name="displayName" />
      <JoinRoomCard v-model:name="displayName" :show-name="false" heading="加入房间" />
    </div>
    <MyRoomsCard />
  </div>

  <!-- ============ 未登录：落地页（开始 → 登录窗口 的过渡） ============ -->
  <div
    class="home landing"
    v-else
    data-testid="home-landing"
    :data-stage="stage"
    :data-transitioning="transitioning ? 'true' : 'false'"
    :class="{ 'is-auth': stage === 'auth' }"
  >
    <!-- 右上角三个入口：GitHub / 作者介绍 / 帮助（都只有图标 → 可访问名 + 跟随鼠标的提示）。 -->
    <div class="landing-top">
      <a
        class="round-btn"
        :href="REPO_URL"
        target="_blank"
        rel="noopener noreferrer"
        aria-label="开源仓库（作者 drayee）"
        v-tip="'开源仓库（作者 drayee）'"
        data-testid="home-github"
      >
        <BrandIcon name="github" decorative :size="18" />
      </a>
      <RouterLink
        class="round-btn"
        :to="{ name: 'about' }"
        aria-label="关于作者"
        v-tip="'关于作者（页面待补充）'"
        data-testid="home-about"
      >
        <BrandIcon name="star" decorative :size="18" />
      </RouterLink>
      <RouterLink
        class="round-btn"
        :to="{ name: 'help' }"
        aria-label="帮助与说明"
        v-tip="'帮助与说明'"
        data-testid="home-help"
      >
        <BrandIcon name="tips" decorative :size="18" />
      </RouterLink>
    </div>

    <div class="landing-stage">
      <!-- 标题块：屏幕中央的大标题；点「开始」后缩小并上移（`lifted`）。 -->
      <header class="hero-head" :class="{ lifted: stage === 'auth' }">
        <!-- 动图槽位：现在没有素材 → 纯 CSS 渐变兜底（不会破图）。见 components/HeroMotion.vue。 -->
        <HeroMotion />
        <h1 class="moon-title" data-testid="home-title">月喵</h1>
        <p class="tagline muted">一起看 · 作者 drayee · 开源</p>
      </header>

      <!-- 变形舞台：「开始」按钮与登录窗口占同一块位置，按钮放大即窗口出现。 -->
      <div
        class="morph"
        :class="{ open: stage === 'auth', 'is-transitioning': transitioning }"
        :style="morphStyle"
      >
        <button
          type="button"
          class="start"
          :class="{ 'is-open': stage === 'auth' }"
          :aria-expanded="stage === 'auth'"
          aria-controls="home-auth-window"
          data-testid="home-start"
          @click="toggleStage"
        >
          <!-- 文字**先**淡掉，按钮外框随后融进窗口：读起来就是"按钮变成了窗口"。 -->
          <span class="start-label" data-testid="home-start-label">开始</span>
        </button>

        <section
          id="home-auth-window"
          ref="authWindow"
          class="auth-window"
          data-testid="home-auth-window"
          :aria-hidden="stage !== 'auth'"
          @transitionend="onWindowTransitionEnd"
        >
          <!-- 内容只在 auth 阶段（以及关闭过渡期间）挂载：hero 阶段 DOM 里没有表单、没有输入框。 -->
          <template v-if="windowContent">
            <div class="window-bar">
              <span class="window-title">
                <BrandIcon name="login" decorative :size="15" />
                登录月喵
              </span>
              <button
                type="button"
                class="round-btn small"
                aria-label="返回（收起登录窗口）"
                v-tip="'返回（收起登录窗口）'"
                data-testid="home-auth-close"
                @click="toggleStage"
              >
                <BrandIcon name="close" decorative :size="16" />
              </button>
            </div>

            <p class="window-hint" v-if="expiredHint" data-testid="home-auth-expired">
              <BrandIcon name="alert-triangle" label="注意" :size="14" />
              {{ expiredHint }}
            </p>

            <AuthPanel plain direct-entry>
              <!-- 免登录进房：与落地页原来的「直接进入房间」是同一张卡片、同一段逻辑。 -->
              <template #direct>
                <JoinRoomCard
                  v-model:name="displayName"
                  heading="直接进入放映室"
                  direct
                  data-testid="home-direct-join"
                />
              </template>
            </AuthPanel>
          </template>
        </section>
      </div>

      <!-- 按钮下方那个小小的、横着的 `>`：上下浮动（reduced-motion 时静止）。 -->
      <span class="chev" aria-hidden="true" data-testid="home-chevron">&gt;</span>
    </div>

    <p class="landing-foot muted small">
      <RouterLink :to="{ name: 'public-rooms' }" class="text-link" data-testid="home-public-rooms">
        <BrandIcon name="video" decorative :size="13" />
        看看公开的房间
      </RouterLink>
      <span>· 看片不需要账号，有房间码就能进</span>
    </p>
  </div>
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

/* 首屏"恢复登录中"：只占一行的中性提示，不摆骨架也不闪落地页。 */
.home.boot {
  min-height: 40vh;
  justify-content: center;
  align-items: center;
}

/* ---------------- 落地页 ---------------- */

.home.landing {
  min-height: 100vh;
  padding: 18px 20px 28px;
  gap: 14px;
}

.landing-top {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

/* 圆形图标按钮（右上角三个入口 / 窗口关闭）：只有图标 → 可访问名 + v-tip。 */
.round-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  padding: 0;
  border-radius: 50%;
  border: 1px solid var(--border);
  background: rgba(31, 31, 39, 0.6);
  color: var(--text-dim);
  text-decoration: none;
  line-height: 0;
}

.round-btn:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}

.round-btn.small {
  width: 30px;
  height: 30px;
}

.landing-stage {
  flex: 1 1 auto;
  display: flex;
  flex-direction: column;
  align-items: center;
  /* 从偏上的位置开始铺：窗口长高时只向下长，整屏不会突然上跳。 */
  justify-content: flex-start;
  padding-top: clamp(24px, 14vh, 140px);
  gap: 20px;
}

/* 标题块是动图槽位的定位上下文。 */
.hero-head {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  /* 与 .morph 的过渡同参数：一起动才像"标题让位给窗口"。 */
  transition:
    transform 460ms cubic-bezier(0.22, 0.61, 0.36, 1),
    opacity 460ms ease;
}

.hero-head.lifted {
  /* 缩小 + 上移一段距离。 */
  transform: translateY(calc(-1 * clamp(48px, 18vh, 190px))) scale(0.34);
  opacity: 0.16;
}

.moon-title {
  position: relative;
  z-index: 1;
  margin: 0;
  font-size: clamp(56px, 13vw, 108px);
  line-height: 1.05;
  letter-spacing: 0.06em;
  /* 标题本身就带一点月夜的冷光，不至于在渐变上糊掉。 */
  text-shadow: 0 6px 30px rgba(0, 161, 214, 0.35);
}

.tagline {
  position: relative;
  z-index: 1;
  margin: 0;
  font-size: 13px;
  letter-spacing: 0.08em;
}

/* 变形舞台：「开始」按钮在流内（它决定 hero 阶段的高度），窗口绝对定位在同一块位置上。 */
.morph {
  position: relative;
  display: flex;
  justify-content: center;
  width: 100%;
  min-height: 56px;
  transition: min-height 440ms cubic-bezier(0.22, 0.61, 0.36, 1);
}

/* 打开后高度跟着窗口的实测高度走（否则绝对定位的窗口会溢出到下方的 `>` 上）。 */
.morph.open {
  min-height: var(--aw-h, 440px);
}

button.start {
  align-self: flex-start;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 168px;
  padding: 14px 46px;
  border-radius: 999px;
  background: var(--accent);
  border: 1px solid var(--accent);
  color: #04141b;
  font-size: 18px;
  font-weight: 700;
  letter-spacing: 0.16em;
  /* 外框比文字晚一点淡出（读起来是"文字先没了、然后按钮长成窗口"）。 */
  transition:
    opacity 420ms ease 240ms,
    transform 320ms ease;
}

button.start:hover {
  transform: translateY(-1px);
}

button.start.is-open {
  opacity: 0;
}

/* 过渡期间：按钮仍然接手点击（连点=打断），窗口暂时不吃事件。 */
.morph.is-transitioning button.start {
  z-index: 3;
}

.morph.is-transitioning .auth-window {
  pointer-events: none;
}

.start-label {
  transition: opacity 220ms ease;
}

button.start.is-open .start-label {
  opacity: 0;
}

/* 登录窗口：半透明 + 背景模糊；从按钮的位置与圆角**放大**出来。
   绝对定位是刻意的：它在 hero 阶段不能参与布局（否则窗口一渲染就把 `>` 顶到屏幕外），
   高度由 `.morph.open` 的 min-height 承接（值来自实测的 `--aw-h`）。 */
.auth-window {
  position: absolute;
  top: 0;
  left: 50%;
  width: min(440px, 100%);
  padding: 16px;
  border-radius: 18px;
  border: 1px solid rgba(255, 255, 255, 0.1);
  background: rgba(23, 23, 29, 0.72);
  backdrop-filter: blur(14px);
  -webkit-backdrop-filter: blur(14px);
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.45);
  transform-origin: top center;
  /* hero 阶段：缩成按钮大小并透明（visibility 也把它移出聚焦序列）。 */
  transform: translateX(-50%) scale(0.5);
  opacity: 0;
  visibility: hidden;
  pointer-events: none;
  transition:
    transform 440ms cubic-bezier(0.22, 0.61, 0.36, 1),
    opacity 300ms ease,
    border-radius 440ms ease,
    visibility 0s linear 440ms;
}

.morph.open .auth-window {
  transform: translateX(-50%) scale(1);
  opacity: 0.96;
  visibility: visible;
  pointer-events: auto;
  transition:
    transform 440ms cubic-bezier(0.22, 0.61, 0.36, 1),
    opacity 340ms ease 120ms,
    border-radius 440ms ease,
    visibility 0s linear 0s;
}

/* hero 阶段圆角更接近胶囊（"从按钮变形"的视觉线索）。 */
.morph:not(.open) .auth-window {
  border-radius: 999px;
}

.window-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 12px;
}

.window-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 15px;
  font-weight: 600;
}

.window-hint {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 12px;
  padding: 8px 10px;
  border-radius: 8px;
  border: 1px solid var(--border);
  background: var(--panel-2);
  color: var(--text-dim);
  font-size: 12px;
  line-height: 1.6;
}

/* 小小的、横着的 `>`：上下浮动。 */
.chev {
  font-size: 22px;
  line-height: 1;
  color: var(--text-dim);
  animation: chev-float 1.5s ease-in-out infinite alternate;
}

.home.is-auth .chev {
  opacity: 0;
  animation: none;
}

@keyframes chev-float {
  from {
    transform: translateY(-5px);
  }
  to {
    transform: translateY(4px);
  }
}

/* auth 阶段把动图槽位压暗，让半透明窗口上的字看得清。 */
.home.landing.is-auth :deep(.hero-motion) {
  opacity: 0.3;
}

.landing-foot {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  flex-wrap: wrap;
  margin: 0;
  text-align: center;
}

.text-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  text-decoration: none;
}

/* ---------------- 已登录外壳 ---------------- */

.capability {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--panel-2);
  color: var(--text-dim);
  font-size: 12px;
  line-height: 1.6;
}

.capability .msg {
  flex: 1;
  min-width: 0;
}

button.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 3px;
  background: transparent;
  border-color: transparent;
  color: inherit;
  line-height: 0;
}

.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
  align-items: start;
}

@media (max-width: 760px) {
  .grid {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 460px) {
  .home {
    padding: 20px 12px 32px;
  }

  .home.landing {
    padding: 14px 12px 22px;
  }

  .auth-window {
    padding: 12px;
  }
}

/* 降级：系统要求减少动效 → 过渡直接判死（阶段切换变成瞬时切换）。 */
@media (prefers-reduced-motion: reduce) {
  .hero-head,
  .morph,
  .auth-window,
  button.start,
  .start-label {
    transition: none;
  }

  .chev {
    animation: none;
  }
}
</style>
