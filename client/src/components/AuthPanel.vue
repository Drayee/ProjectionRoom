<script setup lang="ts">
/**
 * 登录 / 注册 / 忘记密码面板（**唯一**一份表单 UI）。
 *
 * 入口：
 *   - 落地页 `/`（未登录态）的登录窗口 —— 用标签页在登录/注册/忘记密码之间切换，URL 不变，
 *     因为同一个窗口里还要放「直接进入放映室」（免登录进房）；
 *   - `/login`、`/register` 深链与回跳目标 —— 保留，靠 `initialTab` 决定先显示哪一页，
 *     并额外渲染互相跳转的链接（页面上没有标签页之外的切换入口时才需要）。
 *
 * 行为与拆分前逐字一致：本地空值检查省一次注定失败的请求；服务端文案原样展示；
 * 成功后 `router.replace(redirect)` 回发起处。
 *
 * **忘记密码**（本轮范围）：只做入口与表单。后端 SMTP 通道尚未落地，所以提交后
 * 给一句中性的「能力待接入」提示，**不伪造成功**（不会出现"重置邮件已发送"这种假话），
 * 也不发任何请求 —— 表单内容不出浏览器。
 *
 * 既有验收契约（只能新增、不能改名）：`auth-tab-login` / `auth-tab-register` /
 * `login-username` / `login-password` / `login-submit` / `login-error` /
 * `register-username` / `register-displayname` / `register-password` / `register-submit` /
 * `register-error` / `password-policy-hint` / `login-to-register` / `register-to-login`，
 * 以及主行动按钮文案「登录 / 登录中… / 注册 / 注册中…」。
 */
import { computed, nextTick, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { toAuthError } from '../api/auth'
import { useAuthStore } from '../stores/auth'
import { safeRedirectPath } from '../utils/safeRedirect'

/** 表单内的三种页签。 */
type AuthTab = 'login' | 'register' | 'forgot'

const props = withDefaults(
  defineProps<{
    initialTab?: AuthTab
    crossLink?: boolean
    /**
     * 无卡片外框（落地页的登录窗口自己就是那块半透明面板）。
     * 不放 `card` 类，也不画自己的背景/描边。
     */
    plain?: boolean
    /** 是否提供「直接进入放映室」切换（免登录进房）。默认不开，深链页面用它没意义。 */
    directEntry?: boolean
  }>(),
  { initialTab: 'login', crossLink: false, plain: false, directEntry: false },
)

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

/** 当前页签。切换是**本地**的（不导航），落地页才能同时留住「直接进入放映室」。 */
const tab = ref<AuthTab>(props.initialTab)
/** 面板形态：表单 / 免登录进房。 */
const mode = ref<'form' | 'direct'>('form')

const username = ref('')
const displayName = ref('')
const password = ref('')
/** 忘记密码的输入（邮箱或用户名，服务端两者都能定位账号）。 */
const forgotAccount = ref('')
const busy = ref(false)
const error = ref('')
/** 成功态：跳转前先让用户看到"确实成功了"，而不是表单突然消失。 */
const done = ref('')
/** 「能力待接入」这一类**中性**提示（既不是成功也不是失败，所以单独一个槽位）。 */
const capability = ref('')

/** 回跳目标：`?redirect=` 来自 URL，一律过白名单（不是站内路径就回首页）。 */
const redirectTo = computed(() => safeRedirectPath(route.query.redirect))
/** 注册页也要能回到同一个发起处：把 redirect 原样带过去。 */
const redirectQuery = computed(() => ({ redirect: redirectTo.value }))

/** 密码策略（规格 §5）：≥8 位且同时含字母与数字。同一个常量既做提示文案也做校验文案。 */
const PASSWORD_POLICY_TEXT = '密码至少 8 位，且必须同时包含字母与数字'

/** 输出能力本身还没上线（服务端 SMTP）时要说的话：中性、说清边界、不承诺。 */
const FORGOT_PENDING_TEXT =
  '密码重置能力待接入：服务端的邮件（SMTP）通道还没落地，本轮只保留入口与表单；你填的内容不会被发送到任何地方。'

/** 顶部标签页：键盘左右方向键也能切换（Tab 顺序仍然覆盖三个按钮）。 */
const tabs: Array<{ key: AuthTab; text: string; icon: string }> = [
  { key: 'login', text: '登录', icon: 'login' },
  { key: 'register', text: '注册', icon: 'users-group' },
  { key: 'forgot', text: '忘记密码', icon: 'lock' },
]

function setTab(next: AuthTab) {
  tab.value = next
  mode.value = 'form'
  error.value = ''
  done.value = ''
  capability.value = ''
}

function setMode(next: 'form' | 'direct') {
  mode.value = next
  error.value = ''
  done.value = ''
  capability.value = ''
}

/** 左右方向键在**三个**页签之间循环。 */
function onTabKeydown(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const order = tabs.map((t) => t.key)
  const at = order.indexOf(tab.value)
  const step = event.key === 'ArrowRight' ? 1 : order.length - 1
  setTab(order[(at + step) % order.length])
}

/**
 * 提交前的本地策略检查。
 *
 * 与"原样展示服务端文案"不冲突：这是**提交之前**拦住一次注定失败的请求（省一个来回），
 * 服务端仍然有自己的策略检查，它返回的文案照样原样展示（服务端更严时以它为准）。
 * 判据只有一条：这里的判据不能比服务端更严 —— 否则会把服务端接受的密码挡在门外。
 */
function passwordProblem(value: string): string {
  if (value.length < 8) return PASSWORD_POLICY_TEXT
  if (!/[A-Za-z]/.test(value)) return PASSWORD_POLICY_TEXT
  if (!/[0-9]/.test(value)) return PASSWORD_POLICY_TEXT
  return ''
}

onMounted(() => {
  // 已经是登录态还停在登录/注册页没有任何意义（例如拿着旧链接回来）：直接回原目标。
  if (auth.isLoggedIn) {
    void router.replace(redirectTo.value)
  }
})

/** 三个页签各自的 testid：忘记密码**不能**落到 register-* 上（那是既有验收契约）。 */
const submitTestId = computed(() => {
  if (tab.value === 'login') return 'login-submit'
  if (tab.value === 'register') return 'register-submit'
  return 'forgot-submit'
})
const errorTestId = computed(() => {
  if (tab.value === 'login') return 'login-error'
  if (tab.value === 'register') return 'register-error'
  return 'forgot-error'
})
const usernameTestId = computed(() => {
  if (tab.value === 'login') return 'login-username'
  if (tab.value === 'register') return 'register-username'
  return 'forgot-account'
})

/** 主行动按钮文案：登录/注册两档保持原样（验收契约），忘记密码是本轮新增。 */
const submitText = computed(() => {
  if (tab.value === 'login') return busy.value ? '登录中…' : '登录'
  if (tab.value === 'register') return busy.value ? '注册中…' : '注册'
  return '提交'
})

/** 免登录进房面板是否展开（写成 computed 是为了在模板的 `v-if/v-else` 分支里不被类型收窄）。 */
const directOpen = computed(() => mode.value === 'direct')

async function submit() {
  if (busy.value) return
  error.value = ''
  done.value = ''
  capability.value = ''

  // ---------- 忘记密码：只给中性提示，不发请求、不伪造成功 ----------
  if (tab.value === 'forgot') {
    if (forgotAccount.value.trim() === '') {
      error.value = '请填写邮箱或用户名'
      return
    }
    capability.value = FORGOT_PENDING_TEXT
    return
  }

  const name = username.value.trim()
  const isRegister = tab.value === 'register'
  const nick = displayName.value.trim()

  if (isRegister) {
    if (name === '' || nick === '') {
      error.value = '请填写用户名和昵称'
      return
    }
    const weak = passwordProblem(password.value)
    if (weak !== '') {
      error.value = weak
      return
    }
  } else if (name === '' || password.value === '') {
    // 本地空值检查：这不是"重写服务端的错误文案"，而是省掉一次注定失败的请求。
    error.value = '请填写用户名和密码'
    return
  }

  busy.value = true
  try {
    if (isRegister) {
      // 注册成功即是登录态（服务端同时下发 refresh Cookie），无需再登录一次。
      await auth.register({ username: name, displayName: nick, password: password.value })
      done.value = '注册成功，正在进入…'
    } else {
      await auth.login({ username: name, password: password.value })
      done.value = '登录成功，正在进入…'
    }
    // 让成功态真的渲染一帧，再离开这个页面。
    await nextTick()
    await router.replace(redirectTo.value)
  } catch (err) {
    // **原样**展示服务端文案：错误体里的 message 就是给用户看的最终表述，这里不加前缀、
    // 不做归纳。判据④ 要的"用户名或密码错误"与"网络不可达"的区分也是天然成立的 ——
    // 服务端对"用户不存在"和"密码错误"给的是同一句话（不泄漏账号是否存在），
    // 而"连不上"由前端的网络兜底文案给出（AuthApiError.offline）。
    error.value = toAuthError(err).message
    done.value = ''
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="auth-panel" :class="{ card: !props.plain }" data-testid="auth-panel" :data-mode="mode">
    <!-- 免登录进房：与落地页的「直接进入放映室」是同一条路径（由调用方通过 #direct 插槽提供）。 -->
    <template v-if="mode === 'direct'">
      <h1>直接进入放映室</h1>
      <p class="muted sub">按房间码进房看片，<strong>不需要账号</strong>（主播建房才需要登录）。</p>
      <div id="auth-direct-panel" data-testid="auth-direct-panel">
        <slot name="direct" />
      </div>
      <button type="button" class="link-btn" data-testid="auth-direct-back" @click="setMode('form')">
        <BrandIcon name="login" decorative :size="14" />
        返回登录
      </button>
    </template>

    <template v-else>
      <!-- 项目名（月喵）由顶栏承担；这里只放这一屏要做的动作名。 -->
      <h1>{{ tab === 'login' ? '登录' : tab === 'register' ? '注册' : '忘记密码' }}</h1>

      <div class="tabs" role="tablist" aria-label="登录、注册或找回密码" @keydown="onTabKeydown">
        <button
          v-for="item in tabs"
          :key="item.key"
          type="button"
          role="tab"
          class="tab"
          :class="{ active: tab === item.key }"
          :aria-selected="tab === item.key"
          :aria-controls="`auth-panel-${item.key}`"
          :id="`auth-tab-${item.key}`"
          :data-testid="`auth-tab-${item.key}`"
          @click="setTab(item.key)"
        >
          <BrandIcon :name="item.icon" decorative :size="15" />
          {{ item.text }}
        </button>
      </div>

      <div
        class="panel"
        role="tabpanel"
        :id="`auth-panel-${tab}`"
        :aria-labelledby="`auth-tab-${tab}`"
      >
        <p class="muted sub">
          {{
            tab === 'login'
              ? '登录后才能创建房间；只是按房间码进房看片，不需要账号。'
              : tab === 'register'
                ? '注册后自动登录。邮箱字段本期只预留、不验证，所以这里不采集。'
                : '填写邮箱或用户名，我们在此提交重置请求（能力上线前不会真的发信）。'
          }}
        </p>

        <div class="error-bar" v-if="error" :data-testid="errorTestId">
          <BrandIcon name="alert-triangle" label="错误" :size="15" />
          <span class="msg">{{ error }}</span>
          <button
            type="button"
            class="icon-btn"
            aria-label="关闭错误提示"
            v-tip="'关闭错误提示'"
            @click="error = ''"
          >
            <BrandIcon name="close" decorative :size="14" />
          </button>
        </div>

        <!-- 中性提示（"能力待接入"）：既不是错误也不是成功，所以既不红也不绿。 -->
        <div class="pending-bar" v-if="capability" data-testid="forgot-pending">
          <BrandIcon name="tips" label="提示" :size="15" />
          <span class="msg">{{ capability }}</span>
          <span class="badge" data-testid="forgot-pending-state">能力待接入</span>
        </div>

        <p class="ok-bar" v-if="done">
          <BrandIcon name="check-circle" label="成功" :size="15" />
          <span>{{ done }}</span>
        </p>

        <!--
          用 form + submit 按钮而不是 @click：Enter 提交、Tab 顺序、移动端"前往"键
          这三件事都由浏览器原生保证，不需要自己监听 keydown（判据③）。
        -->
        <form novalidate @submit.prevent="submit">
          <!-- 忘记密码只采集"账号标识"一个字段（邮箱或用户名），本轮没有邮件通道，不采集更多。 -->
          <div class="field" v-if="tab === 'forgot'">
            <label for="auth-forgot-account">邮箱或用户名</label>
            <input
              id="auth-forgot-account"
              v-model="forgotAccount"
              name="account"
              autocomplete="username"
              autocapitalize="none"
              autocorrect="off"
              spellcheck="false"
              maxlength="64"
              placeholder="注册时用的邮箱或用户名"
              data-testid="forgot-account"
            />
          </div>

          <div class="field" v-if="tab !== 'forgot'">
            <label :for="`auth-username-${tab}`">用户名</label>
            <input
              :id="`auth-username-${tab}`"
              v-model="username"
              name="username"
              autocomplete="username"
              autocapitalize="none"
              autocorrect="off"
              spellcheck="false"
              maxlength="20"
              :data-testid="usernameTestId"
            />
            <p class="muted hint" v-if="tab === 'register'">3–20 位，只允许字母、数字与下划线；登录时用它。</p>
          </div>

          <div class="field" v-if="tab === 'register'">
            <label for="register-displayname">昵称</label>
            <input
              id="register-displayname"
              v-model="displayName"
              name="nickname"
              autocomplete="nickname"
              maxlength="32"
              data-testid="register-displayname"
            />
            <p class="muted hint">房间里显示的名字，可以随时改（不与用户名绑定）。</p>
          </div>

          <div class="field" v-if="tab !== 'forgot'">
            <label :for="`auth-password-${tab}`">密码</label>
            <input
              :id="`auth-password-${tab}`"
              v-model="password"
              name="password"
              type="password"
              :autocomplete="tab === 'login' ? 'current-password' : 'new-password'"
              :data-testid="tab === 'login' ? 'login-password' : 'register-password'"
            />
            <p class="muted hint" data-testid="password-policy-hint" v-if="tab === 'register'">
              {{ PASSWORD_POLICY_TEXT }}
            </p>
          </div>

          <button
            class="primary"
            type="submit"
            :disabled="busy"
            :aria-busy="busy"
            :data-testid="submitTestId"
          >
            {{ submitText }}
          </button>
        </form>

        <!-- 独立页面（/login、/register）保留互相跳转的链接；落地页有标签页，不需要第二套切换入口。 -->
        <p class="muted note" v-if="crossLink">
          <template v-if="tab === 'login'">
            还没有账号？
            <RouterLink :to="{ name: 'register', query: redirectQuery }" data-testid="login-to-register">
              注册一个
            </RouterLink>
            （注册后自动登录，并回到你刚才要去的页面）。
          </template>
          <template v-else>
            已有账号？
            <RouterLink :to="{ name: 'login', query: redirectQuery }" data-testid="register-to-login">
              去登录
            </RouterLink>
          </template>
        </p>

        <!-- 「直接进入放映室」：免登录进房路径（观众不需要账号）。 -->
        <p class="muted note direct-note" v-if="props.directEntry">
          <button
            type="button"
            class="link-btn"
            data-testid="auth-direct-toggle"
            aria-controls="auth-direct-panel"
            :aria-expanded="directOpen"
            @click="setMode('direct')"
          >
            <BrandIcon name="ticket" decorative :size="14" />
            直接进入放映室
          </button>
          <span class="muted">（观众免登录，输入房间码即可）</span>
        </p>
      </div>
    </template>
  </div>
</template>

<style scoped>
.auth-panel h1 {
  margin: 0 0 12px;
  font-size: 22px;
}

.sub {
  margin: 0 0 16px;
  font-size: 12px;
  line-height: 1.6;
}

.tabs {
  display: flex;
  gap: 6px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 14px;
  flex-wrap: wrap;
}

button.tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: transparent;
  border: 1px solid transparent;
  border-bottom: 2px solid transparent;
  border-radius: 6px 6px 0 0;
  padding: 6px 12px;
  color: var(--text-dim);
  font-size: 13px;
}

button.tab.active {
  color: var(--text);
  border-bottom-color: var(--accent);
}

.error-bar {
  margin-bottom: 14px;
}

.error-bar .msg {
  flex: 1;
  min-width: 0;
}

/* 中性提示条：能力还没上线时说清边界，视觉上刻意与"成功/失败"区分开。 */
.pending-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 14px;
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--panel-2);
  color: var(--text-dim);
  font-size: 12px;
  line-height: 1.6;
}

.pending-bar .msg {
  flex: 1;
  min-width: 0;
}

/* 图标按钮：只由图标承担可点击区，可访问名走 aria-label / 提示走 v-tip。 */
button.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 3px;
  background: transparent;
  border-color: transparent;
  color: inherit;
}

.ok-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 14px;
  color: var(--ok);
  font-size: 12px;
}

.field {
  margin-bottom: 14px;
}

.hint {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.5;
}

button.primary {
  width: 100%;
}

.note {
  margin: 16px 0 0;
  font-size: 12px;
  line-height: 1.6;
}

/* 文字型次级动作（图标 + 文字，不做成只有一个图标的按钮）。 */
.link-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text);
  font-size: 13px;
}

.link-btn:hover:not(:disabled) {
  border-color: var(--accent);
}

.direct-note {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
</style>
