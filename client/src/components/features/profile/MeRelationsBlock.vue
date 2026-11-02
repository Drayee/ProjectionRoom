<script setup lang="ts">
/**
 * `/me` 的关系管理区块（票 #69）。
 *
 * ## 与 `/friends` 的分工
 *
 * 两处看的是**同一批数据**（关注边三个方向 + 黑名单），但用途不同：
 *
 *   `/friends`  找人与处理关系 —— 页头常驻搜索框，搜索是主角；
 *   `/me`       管理自己的关系存量 —— 没有搜索框（要找人去 `/friends`），
 *               只有四份名单与逐行的关系动作。
 *
 * 所以这里**不复用 `FriendsPage`**：那个页面的搜索状态、防抖、URL 的 `?q=`
 * 在管理台里全是噪音。共用的是更下面一层：`api/follows.ts` 与 `api/friends.ts`
 * 两个门面，以及 `ui/UserAvatar` / `ui/Button` / `ui/AsyncState` 三个组件。
 *
 * ## 名单选择进 URL
 *
 * `?tab=relations&list=blocked`（键名唯一源 `utils/meRoute.ts`）。理由与
 * `/friends?tab=` 一致：刷新、分享、后退都免费成立，页面里没有"只改内存的选中态"。
 *
 * ## 授权在服务端（铁律 I3）
 *
 * 本组件不判断"我能不能关注这个人"，照常发请求，403 就照服务端文案说明。
 * 按钮显隐只是少一次注定失败的往返。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import AsyncState from '../../ui/AsyncState.vue'
import Button from '../../ui/Button.vue'
import UserAvatar from '../../ui/UserAvatar.vue'
import * as followsApi from '../../../api/follows'
import * as friendsApi from '../../../api/friends'
import { isFeatureUnavailable } from '../../../api/mode'
import { useAuthStore } from '../../../stores/auth'
import { conversationPath } from '../../../utils/dmRoute'
import { ME_LIST_QUERY, mePath, parseMeList, type MeRelationList } from '../../../utils/meRoute'
import type { FollowListEntry } from '../../../api/follows'
import type { BlockedEntry } from '../../../api/friends'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

/** 四份名单（顺序即界面顺序：好友 → 关注 → 粉丝 → 黑名单）。 */
const LISTS: { key: MeRelationList; label: string }[] = [
  { key: 'mutual', label: '好友' },
  { key: 'following', label: '关注' },
  { key: 'followers', label: '粉丝' },
  { key: 'blocked', label: '黑名单' },
]

/** 当前名单：**唯一真源是 URL**。 */
const list = computed<MeRelationList>(() => parseMeList(route.query[ME_LIST_QUERY]))

const entries = ref<FollowListEntry[]>([])
const blocked = ref<BlockedEntry[]>([])
const busy = ref(false)
const actionBusy = ref(false)
const errorText = ref('')
const notice = ref('')
const blockedUnavailable = ref(false)

/** 渲染用的最小行形状（名单与黑名单统一）。 */
interface Row {
  userId: number
  displayName: string
  username: string
  following: boolean
  followedBy: boolean
  mutual: boolean
  blocked: boolean
}

const rows = computed<Row[]>(() => {
  if (list.value === 'blocked') {
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

const state = computed<'loading' | 'error' | 'empty' | 'ready'>(() => {
  if (errorText.value !== '') return 'error'
  if (busy.value) return 'loading'
  return rows.value.length === 0 ? 'empty' : 'ready'
})

const emptyCopy = computed(() => {
  switch (list.value) {
    case 'mutual':
      return { title: '还没有好友', hint: '互相关注就会成为好友。去好友页找一个人，点「关注」，对方回关即可。' }
    case 'following':
      return { title: '还没有关注任何人', hint: '关注是单向的，不需要对方同意。' }
    case 'followers':
      return { title: '还没有粉丝', hint: '别人关注你之后会出现在这里。' }
    default:
      return { title: '黑名单是空的', hint: '你拉黑的人会出现在这里，并且双方都看不到对方。' }
  }
})

function selectList(next: MeRelationList): void {
  notice.value = ''
  errorText.value = ''
  void router.replace(mePath({ tab: 'relations', list: next }))
}

async function load(): Promise<void> {
  if (!auth.isLoggedIn) return
  busy.value = true
  errorText.value = ''
  blockedUnavailable.value = false
  try {
    if (list.value === 'blocked') {
      blocked.value = await friendsApi.listBlocked()
    } else {
      entries.value = await followsApi.listFollowList(list.value)
    }
  } catch (err) {
    entries.value = []
    blocked.value = []
    if (isFeatureUnavailable(err)) {
      blockedUnavailable.value = true
    } else {
      errorText.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    busy.value = false
  }
}

/** 跑一次写操作并统一处理失败（成功时由调用方写提示）。 */
async function attempt(fn: () => Promise<void>): Promise<boolean> {
  actionBusy.value = true
  errorText.value = ''
  notice.value = ''
  try {
    await fn()
    return true
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
    return false
  } finally {
    actionBusy.value = false
  }
}

async function doFollow(row: Row): Promise<void> {
  if (!(await attempt(() => followsApi.follow(row.userId).then(() => undefined)))) return
  await load()
  notice.value = `已关注 ${row.displayName}`
}

async function doUnfollow(row: Row): Promise<void> {
  if (!(await attempt(() => followsApi.unfollow(row.userId)))) return
  await load()
  notice.value = `已取消关注 ${row.displayName}`
}

async function doBlock(row: Row): Promise<void> {
  if (!(await attempt(() => friendsApi.blockUser(row.userId).then(() => undefined)))) return
  await load()
  notice.value = `已拉黑 ${row.displayName}：对方发不进消息，双方互不可见`
}

async function doUnblock(row: Row): Promise<void> {
  if (!(await attempt(() => friendsApi.unblock(row.userId)))) return
  await load()
  notice.value = `已解除对 ${row.displayName} 的拉黑`
}

function relationHint(row: Row): string {
  if (row.blocked) return '已拉黑'
  if (row.mutual) return '互相关注（好友）'
  if (row.following) return '已关注'
  if (row.followedBy) return '关注了你'
  return ''
}

onMounted(() => {
  void load()
})

/** 登录态恢复完成后再补一次（刷新页面时 `onMounted` 那一刻可能还是游客）。 */
watch(
  () => auth.isLoggedIn,
  (loggedIn) => {
    if (loggedIn && rows.value.length === 0) void load()
  },
)

/** 名单变了就重取（URL 是唯一真源，所以盯 URL 而不是点击）。 */
watch(list, () => {
  notice.value = ''
  errorText.value = ''
  void load()
})
</script>

<template>
  <section class="rel" aria-labelledby="me-relations-head">
    <div class="rel-head">
      <h2 id="me-relations-head" class="sect-title">关系管理</h2>
      <p class="dim small">
        好友 ≡ 互相关注，没有「加好友请求」这回事。要找新的人，去
        <RouterLink :to="{ name: 'friends' }">好友页</RouterLink>。
      </p>
    </div>

    <nav class="lists" role="tablist" aria-label="关系名单">
      <Button
        v-for="l in LISTS"
        :key="l.key"
        variant="ghost"
        class="list-tab"
        role="tab"
        :aria-selected="list === l.key"
        :class="{ on: list === l.key }"
        @click="selectList(l.key)"
      >
        {{ l.label }}
      </Button>
    </nav>

    <p v-if="notice" class="ok" role="status">{{ notice }}</p>
    <p v-if="errorText" class="alert" role="alert">{{ errorText }}</p>

    <p v-if="list === 'blocked' && blockedUnavailable" class="inline-note" role="status">
      服务端暂未提供「黑名单列表」与「解除拉黑」接口：拉黑已经生效，但暂时无法在这里回看名单。
    </p>

    <AsyncState v-else-if="state === 'loading'" state="loading" title="名单加载中…" />

    <AsyncState
      v-else-if="state === 'empty'"
      state="empty"
      icon="users-group"
      :title="emptyCopy.title"
      :hint="emptyCopy.hint"
    >
      <RouterLink class="btn" :to="{ name: 'friends' }">去好友页找人</RouterLink>
    </AsyncState>

    <ul v-else class="rows" data-testid="me-relation-rows">
      <li v-for="r in rows" :key="r.userId" class="row">
        <UserAvatar :user="{ id: r.userId, displayName: r.displayName }" size="md" />
        <div class="who">
          <strong>{{ r.displayName }}</strong>
          <span v-if="r.username" class="small dim">@{{ r.username }}</span>
          <span v-if="relationHint(r)" class="small dim">{{ relationHint(r) }}</span>
        </div>
        <div class="acts">
          <Button v-if="list === 'blocked'" variant="ghost" :disabled="actionBusy" @click="doUnblock(r)">
            解除拉黑
          </Button>
          <template v-else>
            <RouterLink v-if="r.mutual" class="btn" :to="conversationPath(r.userId)">发消息</RouterLink>
            <Button v-if="r.following" variant="ghost" :disabled="actionBusy" @click="doUnfollow(r)">
              已关注
            </Button>
            <Button v-else variant="primary" size="sm" :disabled="actionBusy" @click="doFollow(r)">
              {{ r.followedBy ? '回关' : '关注' }}
            </Button>
            <Button variant="ghost" :disabled="actionBusy" @click="doBlock(r)">拉黑</Button>
          </template>
        </div>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.rel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.rel-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.sect-title {
  margin: 0;
  font-size: 15px;
}
.lists {
  display: flex;
  gap: 2px;
  flex-wrap: wrap;
}
.list-tab {
  --ui-btn-border-color: transparent;
  padding: 6px 16px;
  border-bottom: 2px solid transparent;
  border-radius: 0;
}
.list-tab.on {
  --ui-btn-fg: var(--text);
  --ui-btn-fg-hover: var(--text);
  border-bottom-color: var(--accent);
}
.rows {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.row {
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 10px 14px;
}
.who {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.who strong {
  font-size: 14px;
  overflow-wrap: anywhere;
}
.acts {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
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
.inline-note {
  margin: 0;
  font-size: 13px;
  color: var(--dim);
  border-left: 2px solid var(--line-strong);
  padding-left: 10px;
}
.btn {
  font-size: 13px;
  padding: 5px 10px;
  border: 1px solid var(--line);
  color: var(--text);
  text-decoration: none;
}
.btn:hover {
  background: var(--state-hover);
}
@media (max-width: 560px) {
  .row {
    grid-template-columns: 36px minmax(0, 1fr);
  }
  .acts {
    grid-column: 1 / -1;
    justify-content: flex-start;
  }
}
</style>
