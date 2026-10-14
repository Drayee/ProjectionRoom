<script setup lang="ts">
/**
 * 顶栏。四种形态：
 *   - `shell`   登录后外壳（品牌名 + 分区导航 + 头像）
 *   - `page`    内容页（品牌名 + 返回 + 右侧少量入口）
 *   - `inline`  嵌在卡片里的极简条
 *   - `bare`    只有品牌名（登录/注册壳）
 *
 * 归 ui/：读 stores/auth、用 RouterLink 与 useRouter 导航，不发请求（符合票 #11 的 ui/ 准入判据）。
 *
 * icon-first：导航只留图标 + aria-label + v-tip；文字只在主 CTA 上出现。
 */
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { useAuthStore } from '../../stores/auth'

const props = withDefaults(defineProps<{ variant?: 'shell' | 'page' | 'inline' | 'bare' }>(), {
  variant: 'page',
})

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

const busy = ref(false)

async function doLogout(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await auth.logout()
    await router.push('/')
  } finally {
    busy.value = false
  }
}

/** 头像首字母色：由名字哈希稳定取色（同一用户每次同色）。 */
const avatarColor = computed(() => {
  const name = auth.profile?.displayName || auth.profile?.username || '?'
  let h = 0
  for (const ch of name) h = (h * 31 + ch.codePointAt(0)!) % 360
  return `hsl(${h} 65% 42%)`
})

const avatarLabel = computed(() => {
  const p = auth.profile
  return p ? `${p.displayName || p.username}（个人主页）` : '账号'
})

/** 登录/注册链接带上当前路径，登录后回到原处。 */
const redirectQuery = computed(() => ({ redirect: route.fullPath }))

const NAV = [
  { name: 'public-rooms', icon: 'video', label: '公开房间' },
  { name: 'posts', icon: 'tips', label: '帖子' },
  { name: 'forum', icon: 'chat', label: '番剧论坛' },
  { name: 'friends', icon: 'users-group', label: '好友' },
  { name: 'messages', icon: 'msg', label: '消息' },
  { name: 'help', icon: 'help', label: '帮助' },
] as const
</script>

<template>
  <header class="hdr" :class="`hdr--${props.variant}`">
    <RouterLink class="brand" to="/" aria-label="月喵（返回首页）" data-testid="auth-brand">
      <span class="brand-text">月喵</span>
    </RouterLink>

    <!-- 外壳形态：icon-first 分区导航 -->
    <nav v-if="props.variant === 'shell'" class="nav" aria-label="分区导航">
      <RouterLink
        v-for="item in NAV"
        :key="item.name"
        class="nav-btn"
        :to="{ name: item.name }"
        :aria-label="item.label"
        v-tip="item.label"
      >
        <BrandIcon :name="item.icon" decorative :size="18" />
      </RouterLink>
    </nav>

    <div class="right">
      <RouterLink
        v-if="props.variant === 'page' || props.variant === 'inline'"
        class="nav-btn"
        :to="{ name: 'public-rooms' }"
        aria-label="公开房间"
        v-tip="'公开房间'"
      >
        <BrandIcon name="video" decorative :size="17" />
      </RouterLink>
      <RouterLink
        v-if="props.variant === 'page' || props.variant === 'inline'"
        class="nav-btn"
        :to="{ name: 'help' }"
        aria-label="帮助"
        v-tip="'帮助'"
      >
        <BrandIcon name="help" decorative :size="17" />
      </RouterLink>

      <template v-if="props.variant !== 'bare'">
        <template v-if="auth.isLoggedIn">
          <RouterLink
            v-if="auth.profile?.role === 'admin'"
            class="nav-btn"
            :to="{ name: 'admin' }"
            aria-label="管理端"
            v-tip="'管理端'"
          >
            <BrandIcon name="settings" decorative :size="17" />
          </RouterLink>
          <RouterLink
            class="avatar"
            :to="{ name: 'user', params: { id: String(auth.profile?.id ?? 0) } }"
            :aria-label="avatarLabel"
            :style="{ background: avatarColor }"
          >
            {{ (auth.profile?.displayName || auth.profile?.username || '?').slice(0, 1) }}
          </RouterLink>
          <button
            class="nav-btn"
            type="button"
            :disabled="busy"
            aria-label="退出登录"
            v-tip="'退出登录'"
            @click="doLogout"
          >
            <BrandIcon name="logout" decorative :size="17" />
          </button>
        </template>
        <template v-else>
          <RouterLink class="nav-btn" :to="{ name: 'login', query: redirectQuery }" aria-label="登录" v-tip="'登录'">
            <BrandIcon name="login" decorative :size="17" />
          </RouterLink>
          <RouterLink
            class="nav-btn"
            :to="{ name: 'register', query: redirectQuery }"
            aria-label="注册"
            v-tip="'注册'"
          >
            <BrandIcon name="users-group" decorative :size="17" />
          </RouterLink>
        </template>
      </template>
    </div>
  </header>
</template>

<style scoped>
.hdr {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 18px;
  border-bottom: 1px solid var(--line);
  overflow: hidden;
}

.brand {
  display: flex;
  align-items: center;
  text-decoration: none;
  color: var(--text);
}

.brand:hover {
  text-decoration: none;
}

/* 品牌名用故障黑体：B 的字形本身承担故障美学 */
.brand-text {
  font-family: var(--font-glitch);
  font-size: 24px;
  letter-spacing: 0.04em;
}

/* 顶栏滑过的故障线（常驻；降级时由 tokens.css 判死） */
.hdr::after {
  content: '';
  position: absolute;
  left: 0;
  bottom: 0;
  width: 100%;
  height: 2px;
  background: linear-gradient(90deg, transparent, var(--accent), var(--glitch-r), transparent);
  animation: barline var(--glitch-barline) linear infinite;
}

@keyframes barline {
  from {
    transform: translateX(-100%);
  }
  to {
    transform: translateX(100%);
  }
}

.nav {
  display: flex;
  align-items: center;
  gap: 2px;
  margin-left: 6px;
}

.right {
  display: flex;
  align-items: center;
  gap: 2px;
  margin-left: auto;
}

.nav-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  color: var(--dim);
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
  text-decoration: none;
  transition:
    color var(--dur-micro) var(--ease),
    border-color var(--dur-micro) var(--ease),
    background var(--dur-micro) var(--ease);
}

.nav-btn:hover {
  color: var(--text);
  border-color: var(--line);
  background: rgba(180, 108, 255, 0.08);
  text-decoration: none;
}

.nav-btn:active {
  transform: scale(0.92);
}

.nav-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

/* 图标悬停抖动：故障语言（位置不稳），阶梯而非平滑 */
.nav-btn:hover :deep(.brand-icon) {
  animation: icon-jit var(--glitch-icon-jit) steps(2, end);
}

@keyframes icon-jit {
  0% {
    transform: translateX(0);
  }
  33% {
    transform: translateX(-2px);
  }
  66% {
    transform: translateX(2px);
  }
  100% {
    transform: translateX(0);
  }
}

.avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  color: #fff;
  font-size: 14px;
  font-weight: 700;
  text-decoration: none;
  transition: transform var(--dur-micro) var(--ease);
}

.avatar:hover {
  transform: scale(1.08);
  text-decoration: none;
}

/* bare 形态：只有品牌名，居中偏左 */
.hdr--bare .right {
  display: none;
}

.hdr--inline {
  border-bottom: 0;
  padding: 8px 0;
}

.hdr--inline::after {
  display: none;
}
</style>
