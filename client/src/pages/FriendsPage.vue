<script setup lang="ts">
/**
 * 好友（/friends）。票 #58 重排：**搜索是一种状态，不是一个去处**（决议 #50）。
 *
 * ## 这一版改掉了什么
 *
 * 旧版是四个 tab（好友 / 待处理 / 找朋友 / 黑名单），搜索藏在第三个 tab 里。
 * 症状很具体：**打开好友页看不见搜索框**，而"想加一个人"正是最常见的进入意图。
 * 现在搜索框升为页头常驻，输入即出结果面板（盖住列表区），清空即回列表。
 *
 * 四个 tab 变成「好友（互关）/ 关注 / 粉丝 / 黑名单」——
 * 前三个是**关注边**的三个方向（#42 决议：好友 ≡ 互关，没有独立的请求状态），
 * 第四个仍是 `/api/blocks`（拉黑是另一张表的另一件事）。
 *
 * ## 三条纪律
 *
 *  1. **授权 100% 在服务端**（铁律 I3）。本页不判断"我能不能关注这个人"，
 *     照常发请求，拿 403 就照服务端的文案说明。按钮显隐只是少一次注定失败的往返。
 *  2. **tab 与搜索词都进 URL**（`utils/friendsRoute.ts` 是键名的唯一源）：
 *     分享、刷新、后退键因此自然成立 —— 与 `/messages?peer=` 同一条契约纪律。
 *  3. **危险动作二次确认用页内确认条**（#23 的既有样板）：`window.confirm` 在
 *     headless 验收里是阻塞弹窗，且说不清"这次具体动了什么"。
 *
 * ## 与 `verify-avatar.mjs` 的契约（不能打挂）
 *
 * 那个脚本按 `.row` → `.who strong`（昵称）→ `.avatar` 找好友行，
 * 并断言头像 href 是 `/user/2`、mock 好友列表里有「放映员」。
 * 所以**行结构三个类名必须保留**，`ui/UserAvatar` 仍直接用在行首。
 *
 * ## 与 `/messages` 的跨票契约
 *
 * `utils/dmRoute.ts` 的 `conversationPath(userId)` 生成 `/messages?peer=<id>`。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AsyncState from '../components/ui/AsyncState.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import Button from '../components/ui/Button.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import UserAvatar from '../components/ui/UserAvatar.vue'
import * as followsApi from '../api/follows'
import * as friendsApi from '../api/friends'
import { isFeatureUnavailable } from '../api/mode'
import { useApiFailure } from '../components/features/common/useApiFailure'
import { useAuthStore } from '../stores/auth'
import { conversationPath } from '../utils/dmRoute'
import {
  FRIENDS_DEBOUNCE_MS,
  FRIENDS_Q_QUERY,
  FRIENDS_TAB_QUERY,
  friendsPath,
  parseFriendsQuery,
  parseFriendsTab,
  shouldSearch,
  type FriendsTab,
} from '../utils/friendsRoute'
import type { FollowListEntry, FollowListTab } from '../api/follows'
import type { BlockedEntry, SearchHit } from '../api/friends'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { failure, run } = useApiFailure(() => route.fullPath)

/** 四个 tab 的展示表（顺序即界面顺序）。 */
const TABS: { key: FriendsTab; label: string }[] = [
  { key: 'mutual', label: '好友' },
  { key: 'following', label: '关注' },
  { key: 'followers', label: '粉丝' },
  { key: 'blocked', label: '黑名单' },
]

/** 当前 tab：**唯一真源是 URL**（没有只改内存的选中态）。 */
const tab = computed<FriendsTab>(() => parseFriendsTab(route.query[FRIENDS_TAB_QUERY]))

/**
 * 搜索框里的当前输入（受控）。
 *
 * 与 URL 里的 `q` 不是同一个东西：`q` 是"已经执行过的查询"，
 * 这里是"正在打字的草稿"。两者分开，才能做到"打字时不改 URL、
 * 停下 300ms 才改"（否则每敲一个字都往历史栈里塞一条记录）。
 */
const draft = ref('')

/** 已经执行过的查询词（空串 = 列表态）。 */
const applied = computed(() => parseFriendsQuery(route.query[FRIENDS_Q_QUERY]))

/** 搜索结果面板是否在场。 */
const searching = computed(() => applied.value !== '')

const entries = ref<FollowListEntry[]>([])
const blocked = ref<BlockedEntry[]>([])
const hits = ref<SearchHit[]>([])

const busy = ref(false)
const actionBusy = ref(false)
/** 搜索请求在途（与列表的 `busy` 分开：搜索不该让列表也显示加载中）。 */
const searchBusy = ref(false)
/** 整域未上线（翻成真实实现而后端连不上/没注册）。 */
const capabilityMissing = ref(false)
/** 黑名单是单独的能力缺口（真实实现曾经没有这个接口）。 */
const blockedUnavailable = ref(false)

const notice = ref('')
const actionError = ref('')

/** 搜索结果行的「…」菜单展开的是哪一个（null = 都没展开）。 */
const openMenu = ref<number | null>(null)

/** 危险动作确认条。 */
const pending = ref<null | { kind: 'block'; userId: number; displayName: string }>(null)

/** 搜索框 DOM（空态时要聚焦它）。 */
const searchInput = ref<HTMLInputElement | null>(null)

const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能查看好友'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

/** 列表态：加载中 / 能力缺口 / 失败 / 空 / 就绪（顺序即优先级）。 */
const listState = computed<'loading' | 'capability' | 'error' | 'empty' | 'ready'>(() => {
  if (capabilityMissing.value) return 'capability'
  if (failureText.value) return 'error'
  if (busy.value) return 'loading'
  const rows = tab.value === 'blocked' ? blocked.value : entries.value
  return rows.length === 0 ? 'empty' : 'ready'
})

/** 空态文案：**未搜**与**搜了 0 结果**必须分开说（验收第 4 条）。 */
const emptyCopy = computed(() => {
  switch (tab.value) {
    case 'mutual':
      return { title: '还没有好友', hint: '互相关注就会成为好友。试试上面的搜索框，按用户名或昵称找一个人。' }
    case 'following':
      return { title: '还没有关注任何人', hint: '在搜索框里找一个人，点「关注」即可。关注是单向的，不需要对方同意。' }
    case 'followers':
      return { title: '还没有粉丝', hint: '别人关注你之后会出现在这里。' }
    default:
      return { title: '黑名单是空的', hint: '你拉黑的人会出现在这里，并且双方都看不到对方。' }
  }
})

/** 当前 tab 的名单（黑名单走另一条 API，形状也不同，这里统一成渲染用的最小集）。 */
interface Row {
  userId: number
  displayName: string
  username: string
  /** 关系动作（搜索结果用；名单行也用，因为名单行同样能取关/回关）。 */
  following: boolean
  followedBy: boolean
  mutual: boolean
  blocked: boolean
}

const rows = computed<Row[]>(() => {
  if (tab.value === 'blocked') {
    return blocked.value.map((b) => ({
      userId: b.user.id,
      displayName: b.user.displayName,
      username: '',
      following: false,
      followedBy: false,
      mutual: false,
      blocked: true,
    }))
  }
  return entries.value.map((e) => ({
    userId: e.userId,
    displayName: e.displayName,
    username: e.username,
    following: e.following,
    followedBy: e.followedBy,
    mutual: e.mutual,
    blocked: false,
  }))
})

/** 搜索结果也归一成 Row（`SearchHit` 的四个布尔是可选的，缺省按 relation 回退）。 */
const hitRows = computed<Row[]>(() =>
  hits.value.map((h) => ({
    userId: h.id,
    displayName: h.displayName,
    username: '',
    following: h.following === true,
    followedBy: h.followedBy === true,
    mutual: h.mutual === true,
    blocked: h.blocked === true || h.relation === 'blocked',
  })),
)

/** 确认条里的两句话：动作 + 后果。 */
const confirmTitle = computed(() => `确认拉黑 ${pending.value?.displayName ?? ''}？`)
const confirmConsequence = computed(
  () =>
    '拉黑由服务端强制：对方发不进消息，双方都不能再关注对方，你们在好友、关注与粉丝名单里互不可见。' +
    '公开内容（帖子、档案）仍然可见。拉黑是单向的，只有你能解除。',
)

// ---------- URL 是选中态的唯一真源 ----------

/** 切 tab：写 URL（不是改内存）。 */
function selectTab(next: FriendsTab): void {
  notice.value = ''
  actionError.value = ''
  pending.value = null
  openMenu.value = null
  // 切 tab 保留搜索词：用户在结果里看到一个人，切到「关注」时不该丢掉上下文。
  void router.replace(friendsPath({ tab: next, q: applied.value }))
}

/** 执行一次搜索（写 URL 的 `q`）。 */
function submitSearch(): void {
  const q = draft.value.trim()
  if (!shouldSearch(q)) return
  void router.replace(friendsPath({ tab: tab.value, q }))
}

/** 清空搜索 → 回列表态。 */
function clearSearch(): void {
  draft.value = ''
  hits.value = []
  void router.replace(friendsPath({ tab: tab.value }))
}

// ---------- 防抖自动搜（300ms、≥2 字符） ----------

let debounceTimer: ReturnType<typeof setTimeout> | null = null

function cancelDebounce(): void {
  if (debounceTimer !== null) {
    clearTimeout(debounceTimer)
    debounceTimer = null
  }
}

watch(draft, (next) => {
  cancelDebounce()
  const q = next.trim()
  if (q === '') {
    // 清空 = 立刻回列表（不等防抖：等待会让人以为"没生效"）。
    if (searching.value) clearSearch()
    return
  }
  if (!shouldSearch(q)) return
  debounceTimer = setTimeout(() => {
    debounceTimer = null
    // 只在词真的变了才写 URL：避免"打了一个空格又删掉"也推一条历史。
    if (q !== applied.value) void router.replace(friendsPath({ tab: tab.value, q }))
  }, FRIENDS_DEBOUNCE_MS)
})

// ---------- 取数 ----------

/** 拉当前 tab 的名单。 */
async function loadList(): Promise<void> {
  if (!auth.isLoggedIn) return
  if (tab.value === 'blocked') {
    await loadBlocked()
    return
  }
  busy.value = true
  const loaded = await run(async () => {
    entries.value = await followsApi.listFollowList(tab.value as FollowListTab)
    return true
  })
  busy.value = false
  if (loaded === null) {
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      entries.value = []
    }
    return
  }
  capabilityMissing.value = false
}

/** 黑名单（单独拉：它可能在真实实现里没有接口，不该拖垮整页）。 */
async function loadBlocked(): Promise<void> {
  if (!auth.isLoggedIn) return
  busy.value = true
  try {
    blocked.value = await friendsApi.listBlocked()
    blockedUnavailable.value = false
    capabilityMissing.value = false
  } catch (err) {
    if (isFeatureUnavailable(err)) {
      blockedUnavailable.value = true
      blocked.value = []
    } else {
      actionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    busy.value = false
  }
}

/** 跑一次写操作并统一处理成功/失败。 */
async function attempt(fn: () => Promise<void>): Promise<boolean> {
  actionError.value = ''
  notice.value = ''
  actionBusy.value = true
  const done = await run(fn)
  actionBusy.value = false
  if (done === null) {
    const f = failure.value
    if (f?.kind === 'capability') {
      capabilityMissing.value = true
      return false
    }
    if (f && (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error')) {
      actionError.value = f.message
    }
    return false
  }
  return true
}

/** 搜索（URL 的 `q` 变了就重跑）。 */
async function runSearch(): Promise<void> {
  const q = applied.value
  if (!shouldSearch(q) || !auth.isLoggedIn) {
    hits.value = []
    return
  }
  searchBusy.value = true
  actionError.value = ''
  try {
    hits.value = await friendsApi.searchUsers(q)
  } catch (err) {
    hits.value = []
    if (isFeatureUnavailable(err)) {
      actionError.value = '服务端暂未提供「按用户名找人」接口，这一栏暂不可用。'
    } else {
      actionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    searchBusy.value = false
  }
}

// ---------- 关系动作（结果行 / 名单行共用） ----------

async function doFollow(row: Row): Promise<void> {
  if (!(await attempt(() => followsApi.follow(row.userId).then(() => undefined)))) return
  await refreshAfterWrite()
  notice.value = `已关注 ${row.displayName}`
}

async function doUnfollow(row: Row): Promise<void> {
  if (!(await attempt(() => followsApi.unfollow(row.userId)))) return
  await refreshAfterWrite()
  notice.value = `已取消关注 ${row.displayName}`
}

/** 写完之后按当前视图重取（列表或搜索结果），再补一次提示。 */
async function refreshAfterWrite(): Promise<void> {
  openMenu.value = null
  if (searching.value) await runSearch()
  else await loadList()
}

function askBlock(row: Row): void {
  actionError.value = ''
  notice.value = ''
  openMenu.value = null
  pending.value = { kind: 'block', userId: row.userId, displayName: row.displayName }
}

/** 确认条上点「确认」。 */
async function confirmPending(): Promise<void> {
  const p = pending.value
  if (!p) return
  pending.value = null
  if (!(await attempt(() => friendsApi.blockUser(p.userId).then(() => undefined)))) return
  await refreshAfterWrite()
  notice.value = `已拉黑 ${p.displayName}：对方发不进消息，双方互不可见`
}

/** 解除拉黑。 */
async function unblock(row: Row): Promise<void> {
  if (!(await attempt(() => friendsApi.unblock(row.userId)))) return
  await loadBlocked()
  notice.value = `已解除对 ${row.displayName} 的拉黑`
}

/** 切换「…」菜单。 */
function toggleMenu(userId: number): void {
  openMenu.value = openMenu.value === userId ? null : userId
}

// ---------- 生命周期 ----------

onMounted(() => {
  notice.value = ''
  // 深链带 q：进页即自动执行一次搜索（验收第 1 条）。
  draft.value = applied.value
  if (auth.isLoggedIn) void loadList()
  if (applied.value !== '') void runSearch()
})

/**
 * 登录态翻转后补一次加载。
 *
 * 刷新页面 / 新标签页时 `stores/auth` 还在用 Cookie 恢复会话，
 * `onMounted` 那一刻 `isLoggedIn` 还是 false —— 少了这条，
 * "刷新以后名单一直是空的"会成为一个只在刷新时出现的幽灵 bug。
 */
watch(
  () => auth.isLoggedIn,
  (loggedIn) => {
    if (!loggedIn) return
    if (entries.value.length === 0) void loadList()
    if (applied.value !== '') void runSearch()
  },
)

/** tab 变了就重取（URL 是唯一真源，所以这里盯 URL 而不是点击）。 */
watch(tab, () => {
  notice.value = ''
  actionError.value = ''
  pending.value = null
  openMenu.value = null
  if (auth.isLoggedIn) void loadList()
})

/** 搜索词变了就重跑。 */
watch(applied, (q) => {
  if (q === '') {
    hits.value = []
    return
  }
  if (auth.isLoggedIn) void runSearch()
})

/**
 * 好友列表为空时默认聚焦搜索框（验收第 4 条）。
 *
 * 只在**真的空**且**没在搜**时聚焦：有好友时抢焦点会让键盘用户一进页就
 * 跳过整页内容（焦点顺序被打乱的症状是"Tab 怎么从搜索框开始"）。
 */
watch(
  [listState, searching, tab],
  async ([state, isSearching, currentTab]) => {
    if (state !== 'empty' || isSearching || currentTab === 'blocked') return
    await nextTick()
    searchInput.value?.focus()
  },
  { immediate: false },
)

onBeforeUnmount(cancelDebounce)

/** 搜索结果里的关系文案（`blocked` 优先，因为那时整个不给关注按钮）。 */
function relationHint(row: Row): string {
  if (row.blocked) return '已拉黑'
  if (row.mutual) return '互相关注（好友）'
  if (row.following) return '已关注'
  if (row.followedBy) return '关注了你'
  return ''
}
</script>

<template>
  <div class="page">
    <main class="body">
      <div class="head">
        <h1 class="title">好友</h1>
        <p class="dim sub">关注是单向的，互相关注就是好友。搜索框在上面，随时可用。</p>
      </div>

      <p v-if="auth.restoring" class="dim" role="status">正在恢复登录状态…</p>

      <!-- 未登录：保留**禁用态搜索框**示意（验收第 2 条）—— 直接把搜索框藏起来，
           游客会以为这个功能不存在，而不是以为"需要登录"。 -->
      <section v-else-if="!auth.isLoggedIn" class="gate" role="status">
        <h2 class="gate-head">
          <BrandIcon name="users-group" decorative :size="16" />
          登录后才有好友
        </h2>
        <p class="dim">好友关系挂在账号上，游客身份没有可维护的名单。登录后这里会有搜索框与四份名单。</p>
        <div class="find">
          <input type="search" placeholder="按用户名或昵称找人" aria-label="按用户名搜索（需登录）" disabled />
          <Button variant="primary" disabled>搜索</Button>
        </div>
        <RouterLink class="primary" :to="{ name: 'login', query: { redirect: route.fullPath } }">去登录</RouterLink>
      </section>

      <template v-else>
        <!-- 页头常驻搜索框（决议 #50：搜索是一种状态，不是一个去处）。 -->
        <form class="find" role="search" @submit.prevent="submitSearch">
          <input
            ref="searchInput"
            v-model="draft"
            type="search"
            placeholder="按用户名或昵称找人（至少 2 个字）"
            aria-label="按用户名或昵称搜索"
            :aria-describedby="searching ? 'search-status' : undefined"
          />
          <Button type="submit" variant="primary" :disabled="!shouldSearch(draft.trim())">搜索</Button>
          <Button v-if="searching" variant="ghost" @click="clearSearch">清空</Button>
        </form>

        <!-- 四个 tab（前三个是关注边的三个方向，第四个是另一张表）。 -->
        <nav class="tabs" role="tablist" aria-label="好友分区">
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
            {{ t.label }}
          </Button>
        </nav>

        <p v-if="notice" class="ok" role="status">{{ notice }}</p>
        <p v-if="actionError" class="alert" role="alert">{{ actionError }}</p>

        <!-- 危险动作确认条：动作 + 对象 + 后果，写全再让人点。 -->
        <section v-if="pending" class="confirm" role="alertdialog" aria-labelledby="confirm-head">
          <h2 id="confirm-head" class="confirm-head">{{ confirmTitle }}</h2>
          <p class="confirm-text">{{ confirmConsequence }}</p>
          <div class="confirm-acts">
            <Button variant="primary" :loading="actionBusy" @click="confirmPending">确认</Button>
            <Button variant="ghost" :disabled="actionBusy" @click="pending = null">取消</Button>
          </div>
        </section>

        <!-- ---------- 搜索结果面板（盖住列表区） ---------- -->
        <section v-if="searching" class="sect" aria-label="搜索结果">
          <h2 id="search-status" class="sect-head">
            搜索「{{ applied }}」
            <span v-if="searchBusy" class="small dim">搜索中…</span>
          </h2>

          <!-- 搜了但没结果：文案与"还没搜"刻意不同（验收第 4 条）。 -->
          <AsyncState
            v-if="!searchBusy && hitRows.length === 0"
            state="empty"
            icon="user"
            :title="`没找到「${applied}」`"
            hint="试试更少的字，或者检查一下拼写。也可以去帖子里逛逛，从作者的头像进他的档案。"
          >
            <RouterLink class="btn" to="/posts">去逛帖子</RouterLink>
          </AsyncState>

          <ul v-else-if="hitRows.length > 0" class="rows">
            <li v-for="h in hitRows" :key="h.userId" class="row">
              <UserAvatar :user="{ id: h.userId, displayName: h.displayName }" size="md" />
              <div class="who">
                <strong>{{ h.displayName }}</strong>
                <span v-if="relationHint(h)" class="small dim">{{ relationHint(h) }}</span>
              </div>
              <div class="acts">
                <!-- 关系动作：按关注方向给（验收第 3 条）。
                     拉黑存续期**整个不给关注按钮** —— 点了注定 403。 -->
                <template v-if="h.blocked">
                  <span class="small dim">已拉黑</span>
                </template>
                <template v-else-if="h.mutual">
                  <RouterLink class="btn" :to="conversationPath(h.userId)">发消息</RouterLink>
                  <Button variant="ghost" :disabled="actionBusy" @click="doUnfollow(h)">已关注</Button>
                </template>
                <template v-else-if="h.following">
                  <Button variant="ghost" :disabled="actionBusy" @click="doUnfollow(h)">已关注</Button>
                </template>
                <template v-else-if="h.followedBy">
                  <Button variant="primary" size="sm" :disabled="actionBusy" @click="doFollow(h)">回关</Button>
                </template>
                <template v-else>
                  <Button variant="primary" size="sm" :disabled="actionBusy" @click="doFollow(h)">关注</Button>
                </template>

                <!-- 次级动作：「…」菜单（查看档案 / 拉黑）。 -->
                <div class="menu-wrap">
                  <Button
                    variant="ghost"
                    size="sm"
                    :aria-expanded="openMenu === h.userId"
                    aria-haspopup="menu"
                    :aria-label="`更多关于 ${h.displayName} 的操作`"
                    @click="toggleMenu(h.userId)"
                  >
                    …
                  </Button>
                  <div v-if="openMenu === h.userId" class="menu" role="menu">
                    <RouterLink class="menu-item" role="menuitem" :to="{ name: 'user', params: { id: String(h.userId) } }">
                      查看档案
                    </RouterLink>
                    <button
                      v-if="!h.blocked"
                      type="button"
                      class="menu-item danger"
                      role="menuitem"
                      :disabled="actionBusy"
                      @click="askBlock(h)"
                    >
                      拉黑
                    </button>
                  </div>
                </div>
              </div>
            </li>
          </ul>
        </section>

        <!-- ---------- 名单区（搜索时让位给结果面板） ---------- -->
        <section v-else class="sect" :aria-label="`${TABS.find((t) => t.key === tab)?.label ?? ''}名单`">
          <!-- 黑名单的解释块：拉黑的后果必须写清，而不是只留一个移除按钮。 -->
          <div v-if="tab === 'blocked'" class="explain">
            <h2 class="explain-head">
              <BrandIcon name="alert-triangle" decorative :size="15" />
              拉黑意味着什么
            </h2>
            <ul>
              <li>被拉黑方<strong>发不进消息</strong>（服务端校验关系，不是前端隐藏输入框）。</li>
              <li>双方<strong>都不能</strong>再关注对方（已有的关注边由服务端清理）。</li>
              <li>双方在好友、关注与粉丝名单里<strong>互不可见</strong>。</li>
              <li>公开内容（帖子、档案）仍然可见；拉黑是单向的，只有拉黑方能解除。</li>
            </ul>
          </div>

          <CapabilityNotice
            v-if="listState === 'capability'"
            domain="friends"
            hint="上线后这里是好友（互关）、关注、粉丝三份名单与黑名单。"
            retryable
            @retry="loadList"
          />

          <p v-else-if="tab === 'blocked' && blockedUnavailable" class="inline-note" role="status">
            服务端暂未提供「黑名单列表」与「解除拉黑」接口：拉黑已经生效（后果同上），
            但暂时无法在这里回看名单，也无法解除。这是接口缺口，不是权限问题。
          </p>

          <AsyncState v-else-if="listState === 'loading'" state="loading" title="加载中…" />

          <AsyncState
            v-else-if="listState === 'error'"
            state="error"
            title="名单没加载出来"
            :hint="failureText"
            retryable
            @retry="loadList"
          />

          <AsyncState
            v-else-if="listState === 'empty'"
            state="empty"
            icon="users-group"
            :title="emptyCopy.title"
            :hint="emptyCopy.hint"
          >
            <Button v-if="tab !== 'blocked'" variant="primary" size="sm" @click="searchInput?.focus()">
              去搜索框找人
            </Button>
          </AsyncState>

          <ul v-else class="rows">
            <li v-for="r in rows" :key="r.userId" class="row">
              <UserAvatar :user="{ id: r.userId, displayName: r.displayName }" size="md" />
              <div class="who">
                <strong>{{ r.displayName }}</strong>
                <span v-if="r.username" class="small dim">@{{ r.username }}</span>
              </div>
              <div class="acts">
                <!-- 黑名单只有"解除拉黑"一个动作。 -->
                <Button
                  v-if="tab === 'blocked'"
                  variant="ghost"
                  :disabled="actionBusy"
                  @click="unblock(r)"
                >
                  解除拉黑
                </Button>
                <template v-else>
                  <RouterLink v-if="r.mutual" class="btn" :to="conversationPath(r.userId)">发消息</RouterLink>
                  <Button
                    v-if="r.following"
                    variant="ghost"
                    :disabled="actionBusy"
                    @click="doUnfollow(r)"
                  >
                    已关注
                  </Button>
                  <Button v-else variant="primary" size="sm" :disabled="actionBusy" @click="doFollow(r)">
                    {{ r.followedBy ? '回关' : '关注' }}
                  </Button>
                  <Button variant="ghost" :disabled="actionBusy" @click="askBlock(r)">拉黑</Button>
                </template>
              </div>
            </li>
          </ul>

          <p v-if="tab === 'mutual'" class="foot dim">
            好友 ≡ 互相关注。没有「加好友请求」这回事 —— 关注一个人，对方回关，你们就是好友。
          </p>
        </section>
      </template>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(880px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 16px; }
.head { display: flex; flex-direction: column; gap: 4px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
.sub { margin: 0; font-size: 13px; }

.gate { border: 1px solid var(--line); background: var(--surface); padding: 16px 18px; display: flex; flex-direction: column; gap: 10px; align-items: flex-start; }
.gate-head { margin: 0; font-size: 15px; display: flex; align-items: center; gap: 6px; }

/* 页头常驻搜索框（票 #58）：全宽、与列表同宽。 */
.find { display: flex; gap: 8px; }
.find input { flex: 1; min-width: 0; padding: 8px 10px; border: 1px solid var(--line); background: var(--bg); color: var(--text); font-size: 13px; }
.find input:disabled { opacity: 0.55; cursor: not-allowed; }
.find input:focus-visible { outline: var(--ring); outline-offset: var(--ring-offset); }

/* 分区标签页：芯片外观套在 ui/Button 上。选中态用组件暴露的变量接上，
   不在页面里重写 border/background（状态色只有按钮令牌一个出口）。 */
.tabs { display: flex; gap: 2px; flex-wrap: wrap; }
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

.sect { display: flex; flex-direction: column; gap: 12px; }
.sect-head { margin: 6px 0 0; font-size: 14px; color: var(--dim); display: flex; gap: 8px; align-items: baseline; }

/* 行布局：头像 / 主体 / 动作。560 档动作换行到第二行。
   ⚠ 这三个类名（.row / .who strong / .avatar）是 verify-avatar.mjs 的契约。 */
.rows { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.row {
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 10px 14px;
}
.who { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.who strong { font-size: 14px; overflow-wrap: anywhere; }
.acts { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }

/* 「…」菜单：绝对定位在按钮下方，点外面不会自动关（简单实现，够用）。 */
.menu-wrap { position: relative; }
.menu {
  position: absolute;
  top: calc(100% + 4px);
  right: 0;
  z-index: 3;
  min-width: 132px;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--line-strong);
  background: var(--surface);
  box-shadow: var(--elev-2);
}
.menu-item { padding: 8px 12px; text-align: left; font-size: 13px; color: var(--text); text-decoration: none; background: transparent; border: 0; cursor: pointer; }
.menu-item:hover { background: var(--state-hover); text-decoration: none; }
.menu-item:focus-visible { outline: var(--ring); outline-offset: -2px; }
.menu-item.danger { color: var(--danger); }

/* .btn / .primary 只剩**跳转型**按钮在用（「发消息」与「去登录」是 RouterLink）：
   与 ui/Button 的外观对齐，但保留链接语义。按钮型动作一律走 <Button>。 */
.btn { display: inline-flex; align-items: center; justify-content: center; padding: 6px 14px; border: 1px solid var(--line); color: var(--dim); font-size: 13px; text-decoration: none; }
.btn:hover { color: var(--text); background: var(--state-hover); border-color: var(--accent); text-decoration: none; }
.primary { display: inline-flex; align-items: center; justify-content: center; padding: 8px 18px; background: var(--accent); color: var(--accent-ink); font-weight: 700; font-size: 13px; text-decoration: none; }
.primary:hover { background: var(--accent-hover); text-decoration: none; }

.confirm { border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.1); padding: 12px 16px; display: flex; flex-direction: column; gap: 8px; }
.confirm-head { margin: 0; font-size: 14px; color: #ffdada; }
.confirm-text { margin: 0; font-size: 13px; line-height: 1.6; }
.confirm-acts { display: flex; gap: 8px; }

.explain { border: 1px solid var(--line); background: var(--surface); padding: 12px 16px; }
.explain-head { margin: 0 0 8px; font-size: 13px; color: var(--dim); display: flex; align-items: center; gap: 6px; }
.explain ul { margin: 0; padding-left: 20px; display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--dim); }

.inline-note { margin: 0; padding: 10px 12px; border: 1px dashed var(--line); font-size: 13px; line-height: 1.6; color: var(--dim); }
.ok { margin: 0; padding: 8px 10px; border: 1px solid var(--accent); background: rgba(180, 108, 255, 0.1); font-size: 12px; }
.alert { margin: 0; padding: 8px 10px; border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.12); font-size: 12px; color: #ffdada; }
.small { font-size: 12px; }
.foot { margin: 0; font-size: 12px; }

@media (max-width: 560px) {
  .row { grid-template-columns: 36px minmax(0, 1fr); }
  .acts { grid-column: 1 / -1; justify-content: flex-start; }
  .confirm-acts { flex-wrap: wrap; }
  .find { flex-wrap: wrap; }
}
</style>
