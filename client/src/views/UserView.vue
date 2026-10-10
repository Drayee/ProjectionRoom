<script setup lang="ts">
/**
 * 用户详情页（`/user/:id`）——本轮是**最小页**。
 *
 * 数据来源只有一个：本机已经拿到的那份账号档案（`localStorage['pr:profile']`，
 * 由 `/api/auth/me`、登录/注册/刷新的响应写入）。
 *
 * 为什么不去请求 `/api/users/:id`：**服务端还没有这个接口**（现有的是管理端的
 * `/api/admin/users/...`，非管理员调只会 403）。与其猜一个不存在的端点、或者把 403
 * 当成"用户不存在"显示，不如把边界说清楚：本轮只支持看**自己**的资料，
 * 看别人的资料页要等公开档案接口。
 *
 * 页面内容：昵称 / 用户名 / 角色 / 加入时间 + 退出登录。
 * 「加入时间」来自服务端档案里的 `createdAt`（`usecase.Profile` 一直在返回它）；
 * 缺失时显示"服务端未返回"，不编一个时间出来。
 */
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
import { useAuthStore } from '../stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const busy = ref(false)

/** 路由参数永远是字符串：`/user/12` → `'12'`。 */
const wantedId = computed(() => {
  const raw = Array.isArray(route.params.id) ? route.params.id[0] : route.params.id
  const parsed = Number(raw)
  return Number.isFinite(parsed) ? parsed : NaN
})

/**
 * 是不是"我自己"。
 *
 * 首选判据是 id 比对。档案里没有 id（旧缓存、或被人手改过）时**按本人处理**：
 * 那份缓存本来就是当前这个人的（`pr:profile` 只由本人会话写入），此时把用户挡在
 * "只看得到别人的资料"上反而是错的。
 */
const isSelf = computed(() => {
  const profile = auth.profile
  if (!profile) return false
  if (profile.id < 1) return true
  return Number.isFinite(wantedId.value) && profile.id === wantedId.value
})

const roleText = computed(() => (auth.profile?.role === 'admin' ? '管理员' : '普通用户'))

/** 加入时间：服务端给的是 RFC3339；本地按时区渲染，解析不了就照原样显示（不吞信息）。 */
const joinedAt = computed(() => {
  const raw = auth.profile?.createdAt ?? ''
  if (raw === '') return ''
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return raw
  return date.toLocaleString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
})

const avatarChar = computed(() => {
  const name = auth.displayName.trim()
  return name === '' ? '月' : ([...name][0] ?? '月')
})

async function doLogout() {
  if (busy.value) return
  busy.value = true
  try {
    await auth.logout()
    await router.push('/')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="page" data-testid="user-page">
    <AppHeader variant="page" />

    <!-- 未登录：说清这一页需要登录，并给回首页的路（首页才有登录入口）。 -->
    <section class="card" v-if="!auth.isLoggedIn" data-testid="user-anonymous">
      <h1>还没登录</h1>
      <p class="muted">
        这一页显示的是<strong>你自己的</strong>账号资料，需要先登录。看片不受影响 ——
        <RouterLink :to="{ name: 'public-rooms' }">公开房间</RouterLink> 免登录可进。
      </p>
      <p>
        <RouterLink class="btn" :to="{ name: 'home' }">
          <BrandIcon name="login" decorative :size="15" />
          回首页登录
        </RouterLink>
      </p>
    </section>

    <!-- 别人的资料页：接口还没上线，这里直说。 -->
    <section class="card" v-else-if="!isSelf" data-testid="user-others-unsupported">
      <h1>暂时只看得到自己的资料</h1>
      <p class="muted">
        公开的用户档案接口还没实现（现有的是管理端接口，非管理员调用会被服务端拒绝）。
        所以这一页目前只对<strong>本人</strong>有效。
      </p>
      <p>
        <RouterLink
          class="btn"
          :to="{ name: 'user', params: { id: String(auth.profile?.id ?? 0) } }"
          data-testid="user-to-me"
        >
          <BrandIcon name="users-group" decorative :size="15" />
          看我的资料
        </RouterLink>
      </p>
    </section>

    <template v-else>
      <header class="head">
        <span class="avatar" aria-hidden="true">{{ avatarChar }}</span>
        <div class="head-text">
          <h1 data-testid="user-displayname">{{ auth.displayName }}</h1>
          <p class="muted mono" data-testid="user-username">@{{ auth.profile?.username }}</p>
        </div>
      </header>

      <section class="card">
        <h2>资料</h2>
        <dl class="kv" data-testid="user-profile">
          <dt>昵称</dt>
          <dd data-testid="user-nickname">{{ auth.displayName }}</dd>
          <dt>用户名</dt>
          <dd class="mono">{{ auth.profile?.username }}</dd>
          <dt>角色</dt>
          <dd data-testid="user-role">
            {{ roleText }}
            <span class="muted small">（角色只决定显示什么；能不能操作由服务端判定）</span>
          </dd>
          <dt>加入时间</dt>
          <dd data-testid="user-joined-at">{{ joinedAt !== '' ? joinedAt : '服务端未返回' }}</dd>
        </dl>

        <p class="muted small note">
          后续再加：头像上传、昵称修改、公开资料页、退出所有设备（服务端已有
          <code class="mono">/api/auth/logout-all</code>，界面入口待做）。
        </p>
      </section>

      <section class="card">
        <h2>会话</h2>
        <button
          type="button"
          class="btn danger"
          :disabled="busy"
          :aria-busy="busy"
          data-testid="user-logout"
          @click="doLogout"
        >
          <BrandIcon :name="busy ? 'refresh' : 'logout'" decorative :size="15" :class="{ spinning: busy }" />
          {{ busy ? '正在退出…' : '退出登录' }}
        </button>
      </section>
    </template>
  </div>
</template>

<style scoped>
.page {
  max-width: 760px;
  margin: 0 auto;
  padding: 36px 20px 44px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.head {
  display: flex;
  align-items: center;
  gap: 12px;
}

.avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 52px;
  height: 52px;
  border-radius: 50%;
  background: var(--accent);
  color: #04141b;
  font-size: 24px;
  font-weight: 700;
  flex: none;
}

.head-text h1 {
  margin: 0 0 2px;
  font-size: 22px;
  overflow-wrap: anywhere;
}

.head-text p {
  margin: 0;
  font-size: 13px;
}

.card h1 {
  margin: 0 0 10px;
  font-size: 20px;
}

.card h2 {
  margin: 0 0 12px;
  font-size: 15px;
}

.card p {
  margin: 0 0 12px;
  font-size: 13px;
  line-height: 1.8;
}

.card p:last-child {
  margin-bottom: 0;
}

.kv {
  display: grid;
  grid-template-columns: 84px minmax(0, 1fr);
  gap: 8px 12px;
  margin: 0;
  font-size: 13px;
}

.kv dt {
  color: var(--text-dim);
}

.kv dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.small {
  font-size: 12px;
}

.note {
  margin: 16px 0 0;
  line-height: 1.7;
}

.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 14px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--panel-2);
  color: var(--text);
  text-decoration: none;
  font-size: 13px;
}

.btn:hover {
  border-color: var(--accent);
  text-decoration: none;
}

button.btn.danger {
  color: var(--danger);
}

button.btn.danger:hover:not(:disabled) {
  border-color: var(--danger);
}

.spinning {
  animation: user-spin 0.9s linear infinite;
}

@keyframes user-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 460px) {
  .page {
    padding: 20px 12px 32px;
  }
}
</style>
