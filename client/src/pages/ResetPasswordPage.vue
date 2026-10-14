<script setup lang="ts">
/**
 * 重置密码（/reset#token=…）。骨架阶段最小实现：fragment token 认领 + 提交 + 清会话。
 * 完整交互与分诊由 #19 填充。硬契约（票 #16）：token 从 fragment 读、读后立刻抹 hash、
 * 成功后 auth.clearSession()（安全副作用不可省）、免登录可达。
 */
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppHeader from '../components/ui/AppHeader.vue'
import { reset, toAuthError } from '../api/auth'
import { useAuthStore } from '../stores/auth'
import { takeFragmentToken } from '../utils/fragmentToken'

const auth = useAuthStore()
const router = useRouter()

const token = ref('')
const password = ref('')
const busy = ref(false)
const failed = ref('')
const done = ref(false)

onMounted(() => {
  token.value = takeFragmentToken(window.location, window.history)
})

async function submit() {
  if (busy.value || !token.value || password.value.length < 8) return
  busy.value = true
  failed.value = ''
  try {
    await reset({ token: token.value, password: password.value })
    // 改密必须撤销全部旧会话：不清就会留着旧登录态在新密码下继续活着
    auth.clearSession()
    done.value = true
    setTimeout(() => void router.replace({ name: 'login' }), 1200)
  } catch (err) {
    failed.value = toAuthError(err).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="page">
    <AppHeader variant="bare" />
    <main class="body">
      <p v-if="!token" class="dim">链接不完整：没有取到重置令牌。请重新走一次「忘记密码」。</p>
      <form v-else-if="!done" class="panel" @submit.prevent="submit">
        <h1 class="title">设置新密码</h1>
        <label for="reset-pwd">新密码</label>
        <input id="reset-pwd" v-model="password" type="password" autocomplete="new-password" placeholder="至少 8 位" />
        <p v-if="failed" class="alert" role="alert">{{ failed }}</p>
        <button class="primary" type="submit" :disabled="busy || password.length < 8">重置密码</button>
      </form>
      <p v-else class="dim">密码已重置，正在跳转登录…</p>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; display: flex; align-items: flex-start; justify-content: center; padding: 40px 20px; }
.panel { width: min(440px, 100%); padding: 22px; border: 1px solid var(--line); background: var(--surface); display: flex; flex-direction: column; gap: 8px; }
.title { margin: 0 0 8px; font-family: var(--font-glitch); font-size: 22px; }
.alert { margin: 4px 0; padding: 8px 10px; border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.12); font-size: 12px; color: #ffdada; }
.primary { margin-top: 8px; padding: 11px; background: var(--accent); color: var(--accent-ink); font-weight: 700; }
.primary:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
