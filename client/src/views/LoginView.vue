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
const password = ref('')
const busy = ref(false)
const error = ref('')

/** 回跳目标：`?redirect=` 来自 URL，一律过白名单（不是站内路径就回首页）。 */
const redirectTo = computed(() => safeRedirectPath(route.query.redirect))
/** 注册页也要能回到同一个发起处：把 redirect 原样带过去。 */
const redirectQuery = computed(() => ({ redirect: redirectTo.value }))

onMounted(() => {
  // 已经是登录态还停在登录页没有任何意义（例如拿着旧链接回来）：直接回原目标。
  if (auth.isLoggedIn) {
    void router.replace(redirectTo.value)
  }
})

async function submit() {
  if (busy.value) return
  error.value = ''
  const name = username.value.trim()
  if (name === '' || password.value === '') {
    // 本地空值检查：这不是"重写服务端的错误文案"，而是省掉一次注定失败的请求。
    error.value = '请填写用户名和密码'
    return
  }

  busy.value = true
  try {
    await auth.login({ username: name, password: password.value })
    // 成功就回发起处（判据①②）：redirect 默认 /，所以从首页发起的登录回到首页。
    await router.replace(redirectTo.value)
  } catch (err) {
    // **原样**展示服务端文案：错误体里的 message 就是给用户看的最终表述，这里不加前缀、
    // 不做归纳。判据④ 要的"用户名或密码错误"与"网络不可达"的区分也是天然成立的 ——
    // 服务端对"用户不存在"和"密码错误"给的是同一句话（不泄漏账号是否存在），
    // 而"连不上"由前端的网络兜底文案给出（AuthApiError.offline）。
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
      <h1>登录</h1>
      <p class="muted sub">登录后才能创建房间；只是按房间码进房看片，不需要账号。</p>

      <div class="error-bar" v-if="error" data-testid="login-error">
        <span>{{ error }}</span>
        <button type="button" @click="error = ''">关闭</button>
      </div>

      <!--
        用 form + submit 按钮而不是 @click：Enter 提交、Tab 顺序、移动端"前往"键
        这三件事都由浏览器原生保证，不需要自己监听 keydown（判据③）。
      -->
      <form novalidate @submit.prevent="submit">
        <div class="field">
          <label for="login-username">用户名</label>
          <input
            id="login-username"
            v-model="username"
            name="username"
            autocomplete="username"
            autocapitalize="none"
            autocorrect="off"
            spellcheck="false"
            maxlength="20"
            data-testid="login-username"
          />
        </div>

        <div class="field">
          <label for="login-password">密码</label>
          <input
            id="login-password"
            v-model="password"
            name="password"
            type="password"
            autocomplete="current-password"
            data-testid="login-password"
          />
        </div>

        <button class="primary" type="submit" :disabled="busy" :aria-busy="busy" data-testid="login-submit">
          {{ busy ? '登录中…' : '登录' }}
        </button>
      </form>

      <p class="muted note">
        还没有账号？
        <RouterLink :to="{ name: 'register', query: redirectQuery }" data-testid="login-to-register">
          注册一个
        </RouterLink>
        （注册后自动登录，并回到你刚才要去的页面）。
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

button.primary {
  width: 100%;
}

.note {
  margin: 16px 0 0;
  font-size: 12px;
  line-height: 1.6;
}
</style>
