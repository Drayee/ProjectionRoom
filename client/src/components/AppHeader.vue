<script setup lang="ts">
/**
 * 顶部导航壳：品牌、帮助/GitHub 入口与账号区的**唯一**渲染点
 *（首页、房间页、登录/注册页共用一份，避免三处各写一套"未登录/恢复中/已登录"的判定而慢慢漂移）。
 *
 * 三种形态（一个用法一种，不做"什么都能配"的万能组件）：
 *   - page：首页顶栏；
 *   - inline：房间页头，塞进已有的 `.room-head .right` 那一行里 ——
 *     **不新增一行**，因为房间页是 `height:100vh` 的固定布局，多一行就是从播放器身上抠高度；
 *   - bare：登录/注册页，只放品牌与帮助/GitHub（不放账号区：这一屏本身就是登录入口）。
 *
 * 窄屏（375px）：整个顶栏 `flex-wrap` + 品牌文字在窄屏收起为品牌标（可访问名不丢，
 * 由 `aria-label` 承担），避免出现横向滚动条。
 */
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { useAuthStore } from '../stores/auth'

const props = withDefaults(defineProps<{ variant?: 'page' | 'inline' | 'bare' }>(), { variant: 'page' })

/** 开源仓库地址（作者 drayee）。 */
const REPO_URL = 'https://github.com/Drayee/ProjectionRoom'

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
  <header class="app-bar" :class="props.variant">
    <div class="brand-group">
      <!-- 品牌标走 <img>：它是彩色品牌物，遮罩化会丢掉颜色。alt 留空，可访问名由链接文字承担。 -->
      <RouterLink
        class="brand"
        to="/"
        :aria-label="'月喵（返回首页）'"
        data-testid="auth-brand"
      >
        <img class="logo" src="/icons/夜晚.svg" alt="" width="22" height="22" aria-hidden="true" />
        <span class="brand-text">月喵</span>
      </RouterLink>

      <nav class="quick" aria-label="站点入口">
        <!-- 公开房间列表（二期 T5）：**对所有访客可见**（含未登录）。
             它是"发现房间"的入口，与"按房间码进房"并列，而不是登录后的特权。 -->
        <RouterLink
          class="icon-btn"
          :to="{ name: 'public-rooms' }"
          aria-label="公开房间"
          title="公开房间"
          data-testid="nav-public-rooms"
        >
          <BrandIcon name="video" decorative :size="17" />
        </RouterLink>
        <RouterLink
          class="icon-btn"
          :to="{ name: 'help' }"
          aria-label="帮助与说明"
          title="帮助与说明"
          data-testid="nav-help"
        >
          <BrandIcon name="tips" decorative :size="17" />
        </RouterLink>
        <!-- 管理端（二期 T7）：入口只对管理员显示 —— 但**显示不是授权**。
             服务端才是授权判定的唯一来源（RequireAdmin），非 admin 打开 /admin
             会拿到 403 并看到「无权限」提示。这里读的是本地缓存的 role：它可能过期，
             因此宁可晚显示一步，也绝不据此判断"能不能操作"（一期不变量 I3）。 -->
        <RouterLink
          v-if="!isBare && auth.profile?.role === 'admin'"
          class="icon-btn"
          :to="{ name: 'admin' }"
          aria-label="管理端"
          title="管理端"
          data-testid="nav-admin"
        >
          <BrandIcon name="settings" decorative :size="17" />
        </RouterLink>
        <a
          class="icon-btn"
          :href="REPO_URL"
          target="_blank"
          rel="noopener noreferrer"
          aria-label="开源仓库（作者 drayee）"
          title="开源仓库（作者 drayee）"
          data-testid="nav-github"
        >
          <BrandIcon name="github" decorative :size="17" />
        </a>
      </nav>
    </div>

    <div class="app-nav" :class="props.variant" data-testid="auth-nav">
      <div v-if="!isBare" class="row">
        <!-- 恢复登录中：只在"没有 token 但有档案缓存"的首屏出现（等一次 refresh），
             避免先把已登录的用户显示成"未登录"再变回来。 -->
        <span v-if="auth.restoring" class="muted small" data-testid="auth-restoring">恢复登录中…</span>

        <template v-else-if="auth.isLoggedIn">
          <!-- 昵称一律文本插值：服务端已做字符白名单与去零宽/Bidi（规格 §7.3），前端不再碰 DOM。 -->
          <span class="who" :title="auth.displayName" data-testid="auth-user">{{ auth.displayName }}</span>
          <button
            class="chip icon-btn"
            :disabled="busy"
            :aria-busy="busy"
            :aria-label="busy ? '正在退出登录…' : '退出登录（清除本机会话）'"
            :title="busy ? '正在退出登录…' : '退出登录（清除本机会话）'"
            data-testid="auth-logout"
            @click="doLogout"
          >
            <BrandIcon :name="busy ? 'refresh' : 'logout'" decorative :size="16" :class="{ spinning: busy }" />
          </button>
        </template>

        <template v-else>
          <RouterLink class="link" :to="{ name: 'login', query: toAuth }" data-testid="nav-login">
            <BrandIcon name="login" decorative :size="15" />
            登录
          </RouterLink>
          <RouterLink class="link" :to="{ name: 'register', query: toAuth }" data-testid="nav-register">
            <BrandIcon name="users-group" decorative :size="15" />
            注册
          </RouterLink>
        </template>
      </div>

      <p v-if="auth.notice" class="notice" data-testid="auth-notice">{{ auth.notice }}</p>
    </div>
  </header>
</template>

<style scoped>
.app-bar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px 16px;
  flex-wrap: wrap;
}

.app-bar.inline {
  align-items: center;
  gap: 8px;
}

.brand-group {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  flex-wrap: wrap;
}

.brand {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-weight: 600;
  font-size: 15px;
  text-decoration: none;
  white-space: nowrap;
}

.logo {
  flex: none;
}

.quick {
  display: flex;
  align-items: center;
  gap: 6px;
}

/* 图标按钮的公共外形：点击区靠 padding 撑出来（图标本身只有 16~17px）。 */
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 5px;
  color: var(--text-dim);
  background: transparent;
  border: 1px solid transparent;
  border-radius: 6px;
  text-decoration: none;
  line-height: 0;
}

.icon-btn:hover {
  color: var(--text);
  border-color: var(--border);
  text-decoration: none;
}

.app-nav {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
  min-width: 0;
}

.app-nav.inline {
  align-items: center;
  flex-direction: row;
}

/* 房间页的 inline 形态本来就塞在 `.room-head .right` 那一行里（房间页是定高布局），
   那里已经另有昵称与房间码，品牌只需要留下标记：文字收起，可访问名由链接的 aria-label 承担。 */
.app-bar.inline .brand-text {
  display: none;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.who {
  max-width: 18ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

button.chip {
  padding: 5px;
}

.link {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  color: var(--accent);
  text-decoration: none;
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

.spinning {
  animation: brand-spin 0.9s linear infinite;
}

@keyframes brand-spin {
  to {
    transform: rotate(360deg);
  }
}

/* 375px 档：品牌文字收起为品牌标（链接另有 aria-label），保证顶栏不横向溢出。 */
@media (max-width: 460px) {
  .brand-text {
    display: none;
  }

  .app-bar .app-nav {
    align-items: flex-start;
  }
}
</style>
