<script setup lang="ts">
/**
 * 验证邮箱（/verify#token=…）。骨架阶段最小实现：fragment token 认领 + 自动提交。
 * 完整三分诊（400/429/503）由 #19 填充。硬契约（票 #16）：token 从 fragment 读、
 * 读后立刻抹 hash、免登录可达。
 */
import { onMounted, ref } from 'vue'
import AppHeader from '../components/ui/AppHeader.vue'
import { toAuthError, verifyEmail } from '../api/auth'
import { takeFragmentToken } from '../utils/fragmentToken'

const token = ref('')
const state = ref<'idle' | 'busy' | 'ok' | 'fail'>('idle')
const message = ref('')

onMounted(async () => {
  token.value = takeFragmentToken(window.location, window.history)
  if (!token.value) {
    state.value = 'fail'
    message.value = '链接不完整：没有取到验证令牌。请重新注册或联系管理员绑定邮箱。'
    return
  }
  state.value = 'busy'
  try {
    await verifyEmail({ token: token.value })
    state.value = 'ok'
    message.value = '邮箱已验证。现在可以用它找回密码了。'
  } catch (err) {
    state.value = 'fail'
    message.value = toAuthError(err).message
  }
})
</script>

<template>
  <div class="page">
    <AppHeader variant="bare" />
    <main class="body">
      <p v-if="state === 'busy'" class="dim">正在验证…</p>
      <p v-else :class="state === 'ok' ? 'ok' : 'alert'" role="status">{{ message }}</p>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; display: flex; align-items: center; justify-content: center; padding: 40px 20px; }
.ok { color: var(--ok); }
.alert { color: var(--danger); }
</style>
