<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

/**
 * 导航壳：账号区的**唯一**渲染点（首页、房间页、登录/注册页共用一份，
 * 避免三处各写一套"未登录/登录中/已登录"的判定而慢慢漂移）。
 *
 * 三种形态（一个用法一种，不做"什么都能配"的万能组件）：
 *   - page：首页顶栏，只放账号区（品牌名由首页自己的 <h1> 承担，不重复渲染）；
 *   - inline：房间页头，塞进已有的 `.room-head .right` 那一行里 ——
 *     **不新增一行**，因为房间页是 `height:100vh` 的固定布局，多一行就是从播放器身上抠高度；
 *   - bare：登录/注册页，只放品牌名链接（兼作"返回首页"的出口）。
 */
const props = withDefaults(defineProps<{ variant?: 'page' | 'inline' | 'bare' }>(), { variant: 'page' })

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const busy = ref(false)
const isBare = computed(() => props.variant === 'bare')

/** 登录/注册链接带上**当前路径**：登录成功后回到发起处（判据②，不留死路）。 */
const toAuth = computed(() => ({ redirect: route.fullPath }))

async function doLogout() {
  if (busy.value) return
  busy.value = true
  try {
    // notice 由 store 负责（服务端没收到登出请求时会说清"服务端会话可能仍然有效"）。
    await auth.logout()
    // 判据⑤：登出后回首页，并且此时内存与 sessionStorage 里的 access token 都已清空。
    await router.push('/')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <header v-if="isBare" class="app-bar">
    <RouterLink class="brand" to="/" data-testid="auth-brand">ProjectionRoom</RouterLink>
  </header>

  <div v-else class="app-nav" :class="props.variant" data-testid="auth-nav">
    <div class="row">
      <!-- 恢复登录中：只在"没有 token 但有档案缓存"的首屏出现（等一次 refresh），
           避免先把已登录的用户显示成"未登录"再变回来。 -->
      <span v-if="auth.restoring" class="muted small" data-testid="auth-restoring">恢复登录中…</span>

      <template v-else-if="auth.isLoggedIn">
        <!-- 昵称一律文本插值：服务端已做字符白名单与去零宽/Bidi（规格 §7.3），前端不再碰 DOM。 -->
        <span class="who" :title="auth.displayName" data-testid="auth-user">{{ auth.displayName }}</span>
        <button
          class="chip"
          :disabled="busy"
          :aria-busy="busy"
          title="退出登录（清除本机会话）"
          data-testid="auth-logout"
          @click="doLogout"
        >
          {{ busy ? '退出中…' : '退出' }}
        </button>
      </template>

      <template v-else>
        <RouterLink class="link" :to="{ name: 'login', query: toAuth }" data-testid="nav-login">登录</RouterLink>
        <RouterLink class="link" :to="{ name: 'register', query: toAuth }" data-testid="nav-register">注册</RouterLink>
      </template>
    </div>

    <p v-if="auth.notice" class="notice" data-testid="auth-notice">{{ auth.notice }}</p>
  </div>
</template>

<style scoped>
.app-bar {
  display: flex;
  align-items: center;
}

.brand {
  font-weight: 600;
  font-size: 15px;
  text-decoration: none;
}

.app-nav {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
}

.app-nav.inline {
  align-items: center;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.who {
  max-width: 18ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

button.chip {
  padding: 4px 10px;
  font-size: 12px;
}

.link {
  font-size: 13px;
}

.small {
  font-size: 12px;
}

.notice {
  margin: 0;
  max-width: 420px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--danger);
}
</style>
