<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import { toAuthError } from '../api/auth'
import { useAuthStore } from '../stores/auth'
import { safeRedirectPath } from '../utils/safeRedirect'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const username = ref('')
const displayName = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

/** 回跳目标：`?redirect=` 来自 URL，一律过白名单（不是站内路径就回首页）。 */
const redirectTo = computed(() => safeRedirectPath(route.query.redirect))
const redirectQuery = computed(() => ({ redirect: redirectTo.value }))

/** 密码策略（规格 §5）：≥8 位且同时含字母与数字。同一个常量既做提示文案也做校验文案。 */
const PASSWORD_POLICY_TEXT = '密码至少 8 位，且必须同时包含字母与数字'

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
  if (auth.isLoggedIn) {
    void router.replace(redirectTo.value)
  }
})

async function submit() {
  if (busy.value) return
  error.value = ''
  const name = username.value.trim()
  const nick = displayName.value.trim()
  if (name === '' || nick === '') {
    error.value = '请填写用户名和昵称'
    return
  }
  const weak = passwordProblem(password.value)
  if (weak !== '') {
    error.value = weak
    return
  }

  busy.value = true
  try {
    // 注册成功即是登录态（服务端同时下发 refresh Cookie），无需再登录一次。
    await auth.register({ username: name, displayName: nick, password: password.value })
    await router.replace(redirectTo.value)
  } catch (err) {
    // 服务端文案原样展示（用户名已占用、密码不合策略、限速……都是它说了算）。
    error.value = toAuthError(err).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="auth-page">
    <AppHeader variant="bare" />

    <div class="card auth-card">
      <h1>注册</h1>
      <p class="muted sub">注册后自动登录。邮箱字段本期只预留、不验证，所以这里不采集。</p>

      <div class="error-bar" v-if="error" data-testid="register-error">
        <span>{{ error }}</span>
        <button type="button" @click="error = ''">关闭</button>
      </div>

      <form novalidate @submit.prevent="submit">
        <div class="field">
          <label for="register-username">用户名</label>
          <input
            id="register-username"
            v-model="username"
            name="username"
            autocomplete="username"
            autocapitalize="none"
            autocorrect="off"
            spellcheck="false"
            maxlength="20"
            data-testid="register-username"
          />
          <p class="muted hint">3–20 位，只允许字母、数字与下划线；登录时用它。</p>
        </div>

        <div class="field">
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
          <label for="register-password">密码</label>
          <input
            id="register-password"
            v-model="password"
            name="password"
            type="password"
            autocomplete="new-password"
            data-testid="register-password"
          />
          <p class="muted hint" data-testid="password-policy-hint">{{ PASSWORD_POLICY_TEXT }}</p>
        </div>

        <button class="primary" type="submit" :disabled="busy" :aria-busy="busy" data-testid="register-submit">
          {{ busy ? '注册中…' : '注册' }}
        </button>
      </form>

      <p class="muted note">
        已有账号？
        <RouterLink :to="{ name: 'login', query: redirectQuery }" data-testid="register-to-login">去登录</RouterLink>
      </p>
    </div>
  </div>
</template>

<style scoped>
.auth-page {
  max-width: 460px;
  margin: 0 auto;
  padding: 28px 20px 40px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

h1 {
  margin: 0 0 6px;
  font-size: 22px;
}

.sub {
  margin: 0 0 16px;
  font-size: 12px;
  line-height: 1.6;
}

.error-bar {
  margin-bottom: 14px;
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
