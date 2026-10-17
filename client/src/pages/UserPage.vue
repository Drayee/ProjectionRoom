<script setup lang="ts">
/**
 * 本人资料页（/user/:id）。骨架阶段最小实现：读本地 profile，不发新请求。
 * 公开档案接口尚未实现：非本人 id 显示"暂不支持"。auth.logout() 第二调用点。
 */
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import { useAuthStore } from '../stores/auth'

const props = defineProps<{ id: string }>()

const auth = useAuthStore()
const router = useRouter()

const isSelf = computed(() => auth.profile && String(auth.profile.id) === props.id)
const emailState = computed(() => {
  if (!auth.profile) return '未登录'
  if (!auth.profile.email) return '未绑定邮箱'
  return auth.profile.emailVerified ? '已验证' : '未验证'
})

async function doLogout() {
  await auth.logout()
  await router.push('/')
}
</script>

<template>
  <div class="page">
    <main class="body">
      <h1 class="title">我的账号</h1>
      <p v-if="!auth.profile" class="dim">未登录。看片不需要账号，这里只服务已登录用户。</p>
      <template v-else-if="isSelf">
        <dl class="kv">
          <dt>用户名</dt>
          <dd class="mono">{{ auth.profile.username }}</dd>
          <dt>昵称</dt>
          <dd>{{ auth.profile.displayName }}</dd>
          <dt>邮箱</dt>
          <dd>{{ auth.profile.email || '—' }}</dd>
          <dt>邮箱状态</dt>
          <dd>{{ emailState }}</dd>
          <dt>角色</dt>
          <dd>{{ auth.profile.role }}</dd>
          <dt>注册于</dt>
          <dd class="mono">{{ auth.profile.createdAt ? auth.profile.createdAt.slice(0, 10) : '—' }}</dd>
        </dl>
        <button class="ghost" type="button" @click="doLogout">退出登录</button>
      </template>
      <p v-else class="dim">公开档案接口尚未实现，目前只支持查看本人。</p>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(640px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 16px; align-items: flex-start; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
.kv { margin: 0; display: grid; grid-template-columns: auto 1fr; gap: 8px 16px; border: 1px solid var(--line); background: var(--surface); padding: 16px; width: 100%; }
.kv dt { color: var(--dim); font-size: 13px; }
.kv dd { margin: 0; font-size: 13px; overflow-wrap: anywhere; }
.ghost { padding: 10px 18px; border: 1px solid var(--line); color: var(--dim); }
.ghost:hover { color: var(--text); border-color: var(--accent); }
</style>
