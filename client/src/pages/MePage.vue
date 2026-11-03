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
 * ## 四个分区（`?tab=`，键名唯一源 `utils/meRoute.ts`）
 *
 *   overview   概览：资料编辑（昵称）+ 我的帖子 + 我的放映室
 *   relations  关系管理：好友（互关）/ 关注 / 粉丝 + 黑名单入口
 *   favorites  我的收藏（票 #65）：帖子 / 番剧 / 放映室三份私人书签
 *   settings   账号设置（票 #71）：凭当前密码改密 / 退出所有设备 / 账号只读信息
 *
 * 收藏单独一个分区而不是塞进概览：它是**跨对象**的（三种类型各一套主键），
 * 而概览里那两块都是"我发布的东西"。混在一列会让"我收藏的"与"我发布的"
 * 看起来是同一类关系。
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
 *   - 删帖入口与「我的放映室含未公开/已关闭」已由 #70 接（`DELETE /api/posts/:id`、
 *     `GET /api/rooms/mine`）；帖子块复用他人档案页那一份，只是 `self` 为真时
 *     多出删除按钮 —— 他人页上不该有那个入口，连灰的都不该有；
 *   - 设置区的改密 / 退出所有设备 / 只读信息由 #71 接（`MeSettingsBlock`）；
 *     换绑邮箱（#72）与注销（#73）仍是独立票，那块里明说了"不在这一票"。
 * 所以这里每一块都说明"还差什么"，而不是画一个假的完成态。
 */
import { computed, nextTick, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AsyncState from '../components/ui/AsyncState.vue'
import AvatarImage from '../components/ui/AvatarImage.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import Button from '../components/ui/Button.vue'
import RelativeTime from '../components/ui/RelativeTime.vue'
import UserAvatar from '../components/ui/UserAvatar.vue'
import AuthorPostsBlock from '../components/features/profile/AuthorPostsBlock.vue'
import MeRelationsBlock from '../components/features/profile/MeRelationsBlock.vue'
import MeFavoritesBlock from '../components/features/profile/MeFavoritesBlock.vue'
import MeRoomsBlock from '../components/features/profile/MeRoomsBlock.vue'
import MeSettingsBlock from '../components/features/profile/MeSettingsBlock.vue'
import * as api from '../api/auth'
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
  { key: 'favorites', label: '收藏', icon: 'star-fill' },
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

/**
 * 头像与昵称的显示数据（与 `/user/:id` 同一份派生源）。
 *
 * `avatarUrl` 跟着 `...p` 一起过来（票 #74）：它是服务端事实，
 * 而 `avatarChar` / `avatarColor` 是这里派生的显示层兜底。
 */
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

/**
 * 登录态翻转时读一次注销状态。
 *
 * 放在 watch 而不是 `onMounted`：`/me` 支持深链，而深链进页时 `bootstrap()`
 * 可能还没跑完 —— 挂载那一刻 `isLoggedIn` 还是假，读一次会直接短路。
 * 用 watch（immediate）让"恢复登录 → 登录"这条也覆盖到。
 */
watch(
  () => auth.isLoggedIn,
  (logged) => {
    if (logged) void loadDeletion()
    else deletion.value = null
  },
  { immediate: true },
)

// ---------- 注销冷静期横幅（票 #73） ----------

/**
 * 注销状态：`pending` 为真时页面顶部挂一条撤销横幅。
 *
 * 三条数据来源，优先级从高到低：
 *
 *  1. 本页刚发起/撤销后的服务端响应（最权威，含到期日）；
 *  2. `GET /api/me/deletion`（进页面时确认一次）；
 *  3. `auth.profile.deletionRequestedAt`（登录后 `/me` 已经带回来了，没有到期日）。
 *
 * 为什么需要第 2 条而不是只用第 3 条：横幅要显示"还有 N 天"，N 需要冷静期长度，
 * 那是服务端配置。把它复制到前端就是第二份真源 —— 改配置时前端不改，
 * 横幅会显示一个永远不会到来的日期。
 */
const deletion = ref<api.DeletionState | null>(null)
const deletionLoading = ref(false)
const deletionError = ref('')
const cancelling = ref(false)

/** 冷静期里还剩几天（向上取整；已到期显示 0）。 */
const daysLeft = computed(() => {
  const deadline = deletion.value?.deadline
  if (!deadline) return null
  const ms = new Date(deadline).getTime() - Date.now()
  if (!Number.isFinite(ms)) return null
  return Math.max(0, Math.ceil(ms / 86_400_000))
})

/** 到期日的可读形式（YYYY-MM-DD；解析不了就原样显示）。 */
const deadlineText = computed(() => {
  const deadline = deletion.value?.deadline
  if (!deadline) return ''
  const d = new Date(deadline)
  if (Number.isNaN(d.getTime())) return deadline
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
})

/** 横幅要不要挂：以服务端 pending 为准；本地没有状态时回落到 profile 的标志位。 */
const showDeletionBanner = computed(
  () => deletion.value?.pending === true || (auth.profile?.deletionRequestedAt ?? '') !== '',
)

/**
 * 跟着 `profile.deletionRequestedAt` 刷新横幅。
 *
 * 为什么需要它：发起注销的入口在**设置区**（`MeSettingsBlock`），而横幅在页面顶部。
 * 设置区写完之后只同步了 profile 上的标志位，`deletion`（本页的那份状态）还是旧的 ——
 * 于是横幅会挂出来但**只有标题**：没有"还有 N 天"、也没有到期日，因为那两个值
 * 来自 `GET /api/me/deletion`，而这一页还没查过。
 *
 * 走"标志位变了就补一次查询"而不是让设置区 emit 事件：标志位是**唯一写入点**
 * （`syncDeletionFlag`），它变了就说明状态真的变了 —— 不管是谁改的、也不管
 * 是在哪个标签页改的。emit 只在"本页那个组件"里成立，换个入口就漏。
 */
watch(
  () => auth.profile?.deletionRequestedAt ?? '',
  (flag) => {
    if (flag === '') {
      // 撤销之后横幅立刻撤掉（本页点的、别的标签页点的，都走这里）。
      if (deletion.value?.pending !== true) deletion.value = null
      return
    }
    // 刚发起注销：补一次状态查询，把到期日与剩余天数取回来。
    if (deletion.value?.pending !== true) void loadDeletion()
  },
)

/** 进 /me 时读一次注销状态（只在登录态下）。 */
async function loadDeletion(): Promise<void> {
  if (!auth.isLoggedIn) {
    deletion.value = null
    return
  }
  deletionLoading.value = true
  deletionError.value = ''
  try {
    deletion.value = await auth.loadDeletionState()
  } catch (err) {
    // 读不到状态不该挡住整页：横幅不挂，但把原因留在设置区（用户去那里才知道）。
    deletionError.value = toHttpError(err).message
    deletion.value = null
  } finally {
    deletionLoading.value = false
  }
}

/** 撤销注销：不要密码（撤销是降低风险的方向），成功后横幅立刻消失。 */
async function cancelDeletion(): Promise<void> {
  if (cancelling.value) return
  cancelling.value = true
  deletionError.value = ''
  try {
    deletion.value = await auth.cancelAccountDeletion()
    // 服务端可能回 409（其实没申请过）—— store 已把标志位清掉，这里也清横幅。
    if (deletion.value.pending !== true) deletion.value = null
  } catch (err) {
    deletionError.value = toHttpError(err).message
  } finally {
    cancelling.value = false
  }
}

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
        <!--
          注销冷静期横幅（票 #73）。

          位置刻意在**分区条之上、所有分区之外**：它说的是"整个账号要没了"，
          与用户当前在哪个分区无关。挂在某个分区里会让"切到收藏就看不见"
          成为可能，而那条横幅的唯一作用是让人及时撤销。
        -->
        <section v-if="showDeletionBanner" class="banner" role="alert" aria-labelledby="me-deletion-head">
          <div class="banner-body">
            <h2 id="me-deletion-head" class="banner-title">
              这个账号已申请注销<template v-if="daysLeft !== null">，还有 {{ daysLeft }} 天执行</template>
            </h2>
            <p class="banner-text">
              <template v-if="deadlineText">
                数据将在 <span class="mono">{{ deadlineText }}</span> 之后被永久删除（帖子、私聊、关注关系、放映室一并删除，无法恢复）。
              </template>
              <template v-else>
                数据将在冷静期结束后被永久删除，无法恢复。
              </template>
              在那之前，你可以随时撤销。
            </p>
            <p v-if="deletionError" class="alert" data-testid="deletion-error">{{ deletionError }}</p>
          </div>
          <div class="banner-actions">
            <Button variant="primary" data-testid="deletion-cancel" :disabled="cancelling" @click="cancelDeletion">
              {{ cancelling ? '正在撤销…' : '撤销注销' }}
            </Button>
          </div>
        </section>

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
              <span
                v-if="me"
                class="avatar-lg"
                :style="{ background: me.avatarColor }"
                aria-hidden="true"
                :data-shared="`avatar:${me.id}`"
              >
                {{ me.avatarChar }}
                <AvatarImage :url="me.avatarUrl" />
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
            我的放映室（票 #70）：owner 全部房，含未公开与已关闭。
            与档案页那块不是同一个组件 —— 那一块只列公开且未关闭的房（对外橱窗），
            这一块是资产清单，两个布尔本身就是信息。
          -->
          <MeRoomsBlock />
        </section>

        <!-- ---------- 关系 ---------- -->
        <section v-else-if="tab === 'relations'" class="sect">
          <MeRelationsBlock />
        </section>

        <!-- ---------- 收藏（票 #65） ---------- -->
        <section v-else-if="tab === 'favorites'" class="sect">
          <MeFavoritesBlock />
        </section>

        <!-- ---------- 设置（票 #71） ---------- -->
        <section v-else class="sect" aria-labelledby="me-settings-head">
          <h2 id="me-settings-head" class="sect-title">账号设置</h2>
          <MeSettingsBlock />
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
  /* position + overflow 是像素头像覆盖层（`ui/AvatarImage.vue`）的前提：
     它绝对定位盖在字母上，父级不裁剪就会溢出圆外。 */
  position: relative;
  overflow: hidden;
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
/*
 * 注销横幅（票 #73）。
 *
 * 用 `--danger` 的边框与底色而不是普通卡片：它必须在一屏里被立刻认出来，
 * 而用户来这一页通常是为了做别的事（改昵称、看收藏），不会主动去找它。
 * 但它**不是** modal、不挡住任何东西 —— 撤销是可选的，不该强迫用户先处理它
 * 才能用管理台（与 #71 的「退出所有设备」页内确认条同一取向）。
 */
.banner {
  display: flex;
  gap: 16px;
  align-items: flex-start;
  justify-content: space-between;
  flex-wrap: wrap;
  margin: 0 0 16px;
  padding: 14px 16px;
  border: 1px solid var(--danger);
  border-radius: 10px;
  background: color-mix(in srgb, var(--danger) 8%, transparent);
}
.banner-body {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  flex: 1 1 320px;
}
.banner-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
}
.banner-text {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  color: var(--text-dim);
}
.banner-actions {
  display: flex;
  gap: 8px;
  flex: 0 0 auto;
}
</style>
