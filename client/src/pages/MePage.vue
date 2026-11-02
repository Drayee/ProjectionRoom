<script setup lang="ts">
/**
 * 我的管理台（`/me`，票 #69）。
 *
 * ## 为什么有这一页
 *
 * 决议 #47 把档案拆成双路由：`/user/:id` 是**纯公开视角**（对本人也一样），
 * 管理动作全部收在这里。旧实现在同一条路由里按 `isSelf` 切两套视图，
 * 症状是"同一个地址，登录前后是两个页面"—— 分享与后退都说不清。
 *
 * ## 三个分区（`?tab=`，键名唯一源 `utils/meRoute.ts`）
 *
 *   overview   概览：资料编辑（昵称）+ 我的帖子 + 我的放映室
 *   relations  关系管理：好友（互关）/ 关注 / 粉丝 + 黑名单入口
 *   settings   账号设置 —— **票 #71 填内容**，本票只放槽位说明
 *
 * 概览与关系分成两个分区而不是堆一页：三份名单每份都可能很长，
 * 堆在资料下面会让"改个昵称"要滚过几十行名单。
 *
 * ## 游客
 *
 * `/me` 是纯管理页，没有可看的公开内容 —— 未登录就地渲染登录引导
 * （`AsyncState` 的 empty 形 + 去登录按钮，带 `?redirect=/me`），
 * **不跳转**（与 `/posts?sort=following` 的游客处理同一纪律：
 * 读动作被弹走最烦）。
 *
 * ## 与票 #70 / #71 的边界
 *
 * 本票只做**路由切分与区块骨架**：
 *   - 删帖入口与「我的放映室含未公开/已关闭」由 #70 接（`GET /api/rooms/mine` 还不存在）；
 *   - 设置区五块由 #71 接。
 * 所以这里两块各自说明"还差什么"，而不是画一个假的完成态。
 */
import { computed, nextTick, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AsyncState from '../components/ui/AsyncState.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import Button from '../components/ui/Button.vue'
import RelativeTime from '../components/ui/RelativeTime.vue'
import UserAvatar from '../components/ui/UserAvatar.vue'
import AuthorPostsBlock from '../components/features/profile/AuthorPostsBlock.vue'
import MeRelationsBlock from '../components/features/profile/MeRelationsBlock.vue'
import * as usersApi from '../api/users'
import { NETWORK_MESSAGE, toHttpError } from '../api/http'
import { isCapabilityUnavailable } from '../api/mode'
import { useAuthStore } from '../stores/auth'
import { avatarOf } from '../utils/avatar'
import { ME_TAB_QUERY, mePath, parseMeTab, type MeTab } from '../utils/meRoute'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

/** 三个分区（顺序即界面顺序）。 */
const TABS: { key: MeTab; label: string; icon: string }[] = [
  { key: 'overview', label: '概览', icon: 'user' },
  { key: 'relations', label: '关系', icon: 'users-group' },
  { key: 'settings', label: '设置', icon: 'settings' },
]

/** 当前分区：**唯一真源是 URL**。 */
const tab = computed<MeTab>(() => parseMeTab(route.query[ME_TAB_QUERY]))

function selectTab(next: MeTab): void {
  notice.value = ''
  errorText.value = ''
  void router.replace(mePath({ tab: next }))
}

// ---------- 资料编辑（从 UserPage 搬来，票 #69） ----------

const draft = ref('')
const saving = ref(false)
const notice = ref('')
const errorText = ref('')
const fieldError = ref('')

/** 头像与昵称的显示数据（与 `/user/:id` 同一份派生源）。 */
const me = computed(() => {
  const p = auth.profile
  if (!p) return null
  return { ...p, ...avatarOf(p.id, p.displayName) }
})

const joinedAt = computed(() => {
  const raw = auth.profile?.createdAt ?? ''
  return { raw, missing: raw === '' }
})

const emailState = computed(() => {
  const p = auth.profile
  if (!p) return ''
  if (!p.email) return '未绑定邮箱'
  // 三态：已验证 / 未验证 / 服务端未返回。把 undefined 当成"未验证"是在编造结论。
  if (p.emailVerified === true) return '已验证'
  if (p.emailVerified === false) return '未验证'
  return '服务端未返回验证状态'
})

function roleLabel(role: string): string {
  return role === 'admin' ? '管理员' : '普通用户'
}

/** 公开主页地址：本人档案仍是 `/user/<我的 id>`（决议 #47 的「查看我的公开主页」）。 */
const publicProfilePath = computed(() =>
  auth.profile ? { name: 'user', params: { id: String(auth.profile.id) } } : null,
)

/** 昵称保存：服务端清洗后的权威档案直接落地 store（不再补一发 `/me`）。 */
async function saveDisplayName(): Promise<void> {
  if (saving.value) return
  notice.value = ''
  errorText.value = ''
  fieldError.value = ''
  saving.value = true
  try {
    const updated = await usersApi.updateDisplayName(draft.value)
    auth.applyProfileUpdate(updated)
    draft.value = updated.displayName
    // 提示放在所有刷新之后写（#28 的教训：先写提示再刷新会被重跑的逻辑擦掉）。
    notice.value = `昵称已保存为「${updated.displayName}」`
  } catch (err) {
    if (isCapabilityUnavailable(err)) {
      errorText.value = '服务端尚未提供档案编辑接口。'
      return
    }
    const e = toHttpError(err)
    if (e.status === 401) {
      errorText.value = '登录状态已失效，请重新登录后再试'
      return
    }
    if (e.status === 403) {
      errorText.value = `${e.message}（被封禁的账号不能改昵称；如有疑问请联系管理员）`
      return
    }
    if (e.status === 400) {
      // 400 只可能是昵称不合规：把错误定位到输入框，而不是整页一条报错。
      fieldError.value = e.message
      return
    }
    errorText.value = e.offline ? NETWORK_MESSAGE : e.message
  } finally {
    saving.value = false
  }
}

/** 退出当前设备（与设置区的「退出所有设备」是两件事，后者在票 #71）。 */
async function doLogout(): Promise<void> {
  saving.value = true
  try {
    await auth.logout()
  } finally {
    saving.value = false
  }
}

/**
 * 登录态翻转：登录后把昵称填进输入框，登出后清空。
 *
 * `immediate` 让首次挂载也走这一条 —— 两个入口（挂载 + 登录态变化）
 * 共用同一段逻辑，不会出现"刷新后输入框是空的"这类不对称。
 */
watch(
  () => auth.profile?.displayName ?? '',
  (next) => {
    if (draft.value === '' || !auth.isLoggedIn) draft.value = next
  },
  { immediate: true },
)

// ---------- 分区切换时的焦点管理 ----------

const tabBar = ref<HTMLElement | null>(null)

watch(tab, async () => {
  // 切分区后把焦点送回分区条：键盘用户不该被留在"刚才那个分区里的某个按钮"上。
  await nextTick()
  tabBar.value?.focus()
})
</script>

<template>
  <div class="page">
    <main class="body">
      <div class="head">
        <h1 class="title">我的管理台</h1>
        <p class="dim sub">
          这里管你自己的东西：资料、帖子、放映室、关系与账号设置。
          <RouterLink v-if="publicProfilePath" :to="publicProfilePath">查看我的公开主页</RouterLink>
        </p>
      </div>

      <p v-if="auth.restoring" class="dim" role="status">正在恢复登录状态…</p>

      <!-- 游客：就地登录引导（不跳转 —— 读动作被弹走最烦）。 -->
      <AsyncState
        v-else-if="!auth.isLoggedIn"
        state="empty"
        icon="user"
        title="登录后才能进管理台"
        hint="管理台里的资料、帖子与关系都挂在账号上，游客身份没有可管理的东西。"
      >
        <RouterLink class="primary" :to="{ name: 'login', query: { redirect: route.fullPath } }">去登录</RouterLink>
      </AsyncState>

      <template v-else>
        <!-- 分区条：`?tab=` 进 URL，刷新/分享/后退都成立。 -->
        <nav ref="tabBar" class="tabs" role="tablist" aria-label="管理台分区" tabindex="-1">
          <Button
            v-for="t in TABS"
            :key="t.key"
            variant="ghost"
            class="tab"
            role="tab"
            :aria-selected="tab === t.key"
            :class="{ on: tab === t.key }"
            @click="selectTab(t.key)"
          >
            <BrandIcon :name="t.icon" decorative :size="14" />
            {{ t.label }}
          </Button>
        </nav>

        <!-- ---------- 概览 ---------- -->
        <section v-if="tab === 'overview'" class="sect" aria-labelledby="me-profile-head">
          <div class="card">
            <div class="card-head">
              <span v-if="me" class="avatar-lg" :style="{ background: me.avatarColor }" aria-hidden="true">
                {{ me.avatarChar }}
              </span>
              <div class="who">
                <h2 id="me-profile-head" class="name-row">
                  <span class="name">{{ auth.profile?.displayName || auth.profile?.username }}</span>
                  <span v-if="auth.profile?.role === 'admin'" class="badge">
                    <BrandIcon name="shield-done" decorative :size="12" />
                    管理员
                  </span>
                </h2>
                <p class="mono dim handle">@{{ auth.profile?.username }}</p>
              </div>
            </div>

            <dl class="kv">
              <dt>加入时间</dt>
              <dd class="mono">
                <span v-if="joinedAt.missing">服务端未返回</span>
                <RelativeTime v-else :value="joinedAt.raw" />
              </dd>
              <dt>角色</dt>
              <dd>{{ roleLabel(auth.profile?.role ?? 'user') }}</dd>
              <dt>邮箱</dt>
              <dd>{{ auth.profile?.email || '未绑定邮箱' }}</dd>
              <dt>邮箱状态</dt>
              <dd>{{ emailState }}</dd>
            </dl>

            <form class="edit-form" @submit.prevent="saveDisplayName">
              <label for="me-display-name">昵称</label>
              <div class="edit-row">
                <input
                  id="me-display-name"
                  v-model="draft"
                  type="text"
                  autocomplete="nickname"
                  maxlength="128"
                  :disabled="saving"
                />
                <Button
                  type="submit"
                  variant="primary"
                  :loading="saving"
                  :disabled="saving || draft === (auth.profile?.displayName ?? '')"
                >
                  保存
                </Button>
              </div>
              <p class="dim small">最多 32 个字符；零宽与方向控制字符会被服务端剔除；清空会回落成用户名。</p>
            </form>

            <p v-if="fieldError" class="alert" role="alert">{{ fieldError }}</p>
            <p v-if="errorText" class="alert" role="alert">{{ errorText }}</p>
            <p v-if="notice" class="ok" role="status">{{ notice }}</p>

            <div class="card-foot">
              <Button variant="ghost" :disabled="saving" @click="doLogout">退出登录</Button>
            </div>
          </div>

          <!-- 我的帖子：复用他人档案页那一块（同一份数据、同一套三态）。 -->
          <AuthorPostsBlock :user-id="auth.profile?.id ?? 0" title="我的帖子" self />

          <!--
            我的放映室：票 #70 才接「含未公开与已关闭」的全口径
            （`GET /api/rooms/mine` 尚不存在）。这里先说明缺口，
            而不是只显示已公开的那几间 —— 那会让人以为私密房丢了。
          -->
          <section class="card" aria-labelledby="me-rooms-head">
            <h2 id="me-rooms-head" class="sect-title">我的放映室</h2>
            <p class="dim small">
              「我开过的全部房间」需要服务端提供一个按房主查询的端点（含未公开与已关闭），
              那在票 #70 落地。现在想看自己公开的房，去
              <RouterLink :to="{ name: 'public-rooms' }">公开房间</RouterLink>。
            </p>
          </section>
        </section>

        <!-- ---------- 关系 ---------- -->
        <section v-else-if="tab === 'relations'" class="sect">
          <MeRelationsBlock />
        </section>

        <!-- ---------- 设置（票 #71 填内容） ---------- -->
        <section v-else class="sect" aria-labelledby="me-settings-head">
          <div class="card">
            <h2 id="me-settings-head" class="sect-title">账号设置</h2>
            <p class="dim small">
              改密码（凭当前密码）、退出所有设备、换绑邮箱、账号只读信息与注销，
              都在票 #71 落地。这个分区的地址（<code class="mono">/me?tab=settings</code>）
              已经钉住，届时只往里填内容，不改地址形态。
            </p>
            <ul class="dim small">
              <li>记得密码 → 这里凭当前密码改；</li>
              <li>忘了密码 → 登录页的「找回密码」走邮箱验证码（两条路并存，互不替代）。</li>
            </ul>
          </div>
        </section>
      </template>
    </main>
  </div>
</template>

<style scoped>
.page {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}
.body {
  flex: 1;
  width: min(880px, 100%);
  margin: 0 auto;
  padding: 32px 20px 64px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(32px, 6vw, 48px);
}
.sub {
  margin: 0;
  font-size: 13px;
}
.sub a {
  color: var(--accent);
}

.tabs {
  display: flex;
  gap: 2px;
  flex-wrap: wrap;
  outline: none;
}
.tab {
  --ui-btn-border-color: transparent;
  gap: 6px;
  padding: 6px 16px;
  border-bottom: 2px solid transparent;
  border-radius: 0;
}
.tab.on {
  --ui-btn-fg: var(--text);
  --ui-btn-fg-hover: var(--text);
  border-bottom-color: var(--accent);
}

.sect {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.card {
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 18px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.card-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.avatar-lg {
  width: 56px;
  height: 56px;
  border-radius: 50%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 22px;
  font-weight: 600;
  color: #101014;
  flex: 0 0 auto;
}
.who {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.name-row {
  margin: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 20px;
}
.name {
  overflow-wrap: anywhere;
}
.badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  border: 1px solid var(--line-strong);
  padding: 1px 6px;
  color: var(--dim);
}
.handle {
  margin: 0;
  font-size: 12px;
}
.kv {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 6px 16px;
  margin: 0;
  font-size: 13px;
}
.kv dt {
  color: var(--dim);
}
.kv dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.sect-title {
  margin: 0;
  font-size: 15px;
}
.edit-form {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.edit-form label {
  font-size: 13px;
  color: var(--dim);
}
.edit-row {
  display: flex;
  gap: 8px;
}
.edit-row input {
  flex: 1;
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--line);
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
}
.edit-row input:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}
.card-foot {
  display: flex;
  justify-content: flex-end;
}
.ok {
  margin: 0;
  font-size: 13px;
  color: var(--ok);
}
.alert {
  margin: 0;
  font-size: 13px;
  color: var(--danger);
}
.small {
  font-size: 12px;
}
</style>
