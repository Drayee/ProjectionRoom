<script setup lang="ts">
/**
 * 登录 / 注册面板（**唯一**一份表单 UI）。
 *
 * 三处入口共用它，不再各写一套：
 *   - 落地页 `/`（未登录态）—— 用标签页在登录/注册之间切换，URL 不变，
 *     因为同一个屏里还要放「直接进入房间」；
 *   - `/login`、`/register` 深链与回跳目标 —— 保留，靠 `initialTab` 决定先显示哪一页，
 *     并额外渲染互相跳转的链接（页面上没有标签页之外的切换入口时才需要）。
 *
 * 行为与拆分前逐字一致：本地空值检查省一次注定失败的请求；服务端文案原样展示；
 * 成功后 `router.replace(redirect)` 回发起处。
 */
import { computed, nextTick, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { toAuthError } from '../api/auth'
import { useAuthStore } from '../stores/auth'
import { safeRedirectPath } from '../utils/safeRedirect'

const props = withDefaults(defineProps<{ initialTab?: 'login' | 'register'; crossLink?: boolean }>(), {
  initialTab: 'login',
  crossLink: false,
})

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

/** 当前标签页。切换是**本地**的（不导航），落地页才能同时留住「直接进入房间」。 */
const tab = ref<'login' | 'register'>(props.initialTab)

const username = ref('')
const displayName = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')
/** 成功态：跳转前先让用户看到"确实成功了"，而不是表单突然消失。 */
const done = ref('')

/** 回跳目标：`?redirect=` 来自 URL，一律过白名单（不是站内路径就回首页）。 */
const redirectTo = computed(() => safeRedirectPath(route.query.redirect))
/** 注册页也要能回到同一个发起处：把 redirect 原样带过去。 */
const redirectQuery = computed(() => ({ redirect: redirectTo.value }))

/** 密码策略（规格 §5）：≥8 位且同时含字母与数字。同一个常量既做提示文案也做校验文案。 */
const PASSWORD_POLICY_TEXT = '密码至少 8 位，且必须同时包含字母与数字'

/** 顶部标签页：键盘左右方向键也能切换（Tab 顺序仍然覆盖两个按钮）。 */
const tabs = [
  { key: 'login' as const, text: '登录' },
  { key: 'register' as const, text: '注册' },
]

function setTab(next: 'login' | 'register') {
  tab.value = next
  error.value = ''
  done.value = ''
}

function onTabKeydown(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  setTab(tab.value === 'login' ? 'register' : 'login')
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

async function submit() {
  if (busy.value) return
  error.value = ''
  done.value = ''

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
  <div class="auth-panel card">
    <!-- 项目名（月喵）由顶栏承担；这里只放这一屏要做的动作名。 -->
    <h1>{{ tab === 'login' ? '登录' : '注册' }}</h1>

    <div class="tabs" role="tablist" aria-label="登录或注册" @keydown="onTabKeydown">
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
        <BrandIcon :name="item.key === 'login' ? 'login' : 'users-group'" decorative :size="15" />
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
            : '注册后自动登录。邮箱字段本期只预留、不验证，所以这里不采集。'
        }}
      </p>

      <div class="error-bar" v-if="error" :data-testid="tab === 'login' ? 'login-error' : 'register-error'">
        <BrandIcon name="alert-triangle" label="错误" :size="15" />
        <span class="msg">{{ error }}</span>
        <button
          type="button"
          class="icon-btn"
          aria-label="关闭错误提示"
          title="关闭错误提示"
          @click="error = ''"
        >
          <BrandIcon name="close" decorative :size="14" />
        </button>
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
        <div class="field">
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
            :data-testid="tab === 'login' ? 'login-username' : 'register-username'"
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

        <div class="field">
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
          :data-testid="tab === 'login' ? 'login-submit' : 'register-submit'"
        >
          {{ busy ? (tab === 'login' ? '登录中…' : '注册中…') : tab === 'login' ? '登录' : '注册' }}
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
    </div>
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

/* 图标按钮：只由图标承担可点击区，可访问名走 aria-label / title。 */
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
</style>
