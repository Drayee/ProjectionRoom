<script setup lang="ts">
/**
 * 好友（/friends）。wayfinder #28：把占位页换成真页面。
 *
 * 四个动作 + 两个入口，全部只经由门面 `api/friends.ts`（默认 mock，`VITE_MOCK_FRIENDS=0`
 * 时走真实接口 #29）。三条纪律：
 *
 *  1. **授权 100% 在服务端**（铁律 I3）。本页不判断"我能不能删这个人"，
 *     照常发请求，拿 403/404 就照服务端的文案说明。按钮显隐只是少一次注定失败的往返。
 *  2. **危险动作二次确认用页内确认条，不用 `window.confirm`**（#23 的既有样板）：
 *     `confirm` 在 headless 验收里是阻塞弹窗，且说不清"这次具体动了什么"。
 *     确认条把**动作 + 对象 + 后果**原样写出来 —— 拉黑尤其要说清后果（验收第 2 条）。
 *  3. **"整域未上线"与"单个动作没接口"分开渲染**：前者整页 `CapabilityNotice`，
 *     后者只在那一栏原地说明。把后者也当成整域未上线，好友列表会因为"黑名单读不到"
 *     整页消失 —— 用户会以为好友功能全废（见 `api/mode.ts` 的 `FeatureUnavailable`）。
 *
 * 与 `/messages` 的跨票契约：`utils/dmRoute.ts` 的 `conversationPath(userId)`。
 * 本页的「发消息」按它生成 `/messages?peer=<id>`，票 #30 解析同一个键。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import * as friendsApi from '../api/friends'
import { isFeatureUnavailable } from '../api/mode'
import { useApiFailure } from '../components/features/common/useApiFailure'
import { useAuthStore } from '../stores/auth'
import { conversationPath } from '../utils/dmRoute'
import type { BlockedEntry, FriendEntry, FriendRequest, FriendRequests, RelationState, SearchHit } from '../api/friends'

const route = useRoute()
const auth = useAuthStore()
const { failure, run } = useApiFailure(() => route.fullPath)

type Section = 'friends' | 'requests' | 'find' | 'blocked'

const section = ref<Section>('friends')

const friends = ref<FriendEntry[]>([])
const requests = ref<FriendRequests>({ incoming: [], outgoing: [] })
const blocked = ref<BlockedEntry[]>([])
const hits = ref<SearchHit[]>([])

const query = ref('')
/** 搜索跑过一次（用于区分"还没搜"与"搜了没结果"这两种空态）。 */
const searched = ref(false)

const busy = ref(false)
const actionBusy = ref(false)
/** 整域未上线（翻成真实实现而后端连不上/没注册）。 */
const capabilityMissing = ref(false)
/** 单项能力缺口：域在线，但服务端没有这个接口。 */
const searchUnavailable = ref(false)
const blockedUnavailable = ref(false)
const blockedLoaded = ref(false)

/** 成功提示（一次性，下一次动作/切换分区就清）。 */
const notice = ref('')
/** 写操作失败文案（服务端原样）。 */
const actionError = ref('')

/**
 * 危险动作的确认条。
 *
 * `relationshipId` 只对"删好友"有意义；`userId` 只对"拉黑"有意义（拉黑按人，不按关系）。
 * 两个都存着，是为了让确认条能把"到底对谁、做哪件事"完整写出来。
 */
const pending = ref<null | {
  kind: 'block' | 'remove'
  userId: number
  displayName: string
  relationshipId: number
}>(null)

const incomingCount = computed(() => requests.value.incoming.length)

/** 失败态的一行文案（分诊结果，见 useApiFailure）。 */
const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能查看好友'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

/** 确认条里的两句话：动作 + 后果。拉黑必须说清后果（验收第 2 条）。 */
const confirmTitle = computed(() =>
  pending.value?.kind === 'block' ? `确认拉黑 ${pending.value.displayName}？` : `确认删除好友 ${pending.value?.displayName ?? ''}？`,
)
const confirmConsequence = computed(() => {
  if (!pending.value) return ''
  return pending.value.kind === 'block'
    ? '拉黑由服务端强制：对方发不进消息、发不出好友请求，双方在好友与请求列表里互不可见；现有的好友关系或待处理请求会被这一行覆盖。'
    : '删除后双方都不再在好友列表里；要重新加需要再发一次好友请求并由对方接受。'
})

/** 关系状态的中文文案：搜索结果里"我和他是什么关系"直接决定按钮语义。 */
function relationLabel(state: RelationState): string {
  switch (state) {
    case 'friend':
      return '已是好友'
    case 'pending-out':
      return '请求已发出'
    case 'pending-in':
      return '对方已请求你（去「待处理」处理）'
    case 'blocked':
      return '已拉黑'
    default:
      return '还不是好友'
  }
}

/** 在线态文案：`unknown` 返回空串（宁可不显示，也不把"不知道"显示成"离线"）。 */
function presenceLabel(presence: string): string {
  if (presence === 'online') return '在线'
  if (presence === 'offline') return '离线'
  return ''
}

/**
 * 跑一次写操作并统一处理成功/失败。
 *
 * 返回 true = 成功。失败时把服务端文案放进 `actionError`（**原地**说明），
 * 绝不跳走 —— 用户正在这一页做事，跳走会让他不知道刚才那一下到底成没成。
 */
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

/** 拉好友列表 + 待处理请求（一次两个读端点，两个都是域探针）。 */
async function loadAll(): Promise<void> {
  if (!auth.isLoggedIn) return
  busy.value = true
  const loaded = await run(async () => {
    const [list, reqs] = await Promise.all([friendsApi.listFriends(), friendsApi.listRequests()])
    friends.value = list
    requests.value = reqs
    return true
  })
  busy.value = false
  if (loaded === null && failure.value?.kind === 'capability') {
    capabilityMissing.value = true
    friends.value = []
    requests.value = { incoming: [], outgoing: [] }
    return
  }
  capabilityMissing.value = false
}

/** 黑名单（单独拉：真实实现没有这个接口，不能让它的失败拖垮整页）。 */
async function loadBlocked(): Promise<void> {
  if (!auth.isLoggedIn) return
  actionError.value = ''
  try {
    blocked.value = await friendsApi.listBlocked()
    blockedUnavailable.value = false
  } catch (err) {
    if (isFeatureUnavailable(err)) {
      blockedUnavailable.value = true
      blocked.value = []
    } else {
      actionError.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    // 无论哪条分支都要落地：否则缺口/失败那两条路径会永远停在「加载中…」。
    blockedLoaded.value = true
  }
}

/**
 * 进「找朋友」时先探一次"这个接口在不在"。
 *
 * 为什么要有这次探针：真实实现里 `searchUsers` 是**本地就抛** `FeatureUnavailable`（服务端没有
 * 路由），而页面若等到用户敲完查询才去撞它，用户会先白输一次、再看到"接口没有"。
 * 一次空查询的探针能在**进入分区时**就把话说清：mock 返回空数组（无副作用），
 * 真实实现当场抛 —— 两种实现共用同一条入口，不必在页面里判断 `isMock`。
 */
async function probeSearch(): Promise<void> {
  try {
    await friendsApi.searchUsers('')
    searchUnavailable.value = false
  } catch (err) {
    if (isFeatureUnavailable(err)) searchUnavailable.value = true
  }
}

/** 按用户名找人。空查询不发请求（服务端将来也会拒空 q）。 */
async function doSearch(): Promise<void> {
  const q = query.value.trim()
  if (q === '') return
  actionError.value = ''
  notice.value = ''
  try {
    hits.value = await friendsApi.searchUsers(q)
    searchUnavailable.value = false
  } catch (err) {
    if (isFeatureUnavailable(err)) {
      searchUnavailable.value = true
      hits.value = []
      return
    }
    hits.value = []
    actionError.value = err instanceof Error ? err.message : String(err)
  }
  searched.value = true
}

// ---------- 四个动作 ----------

/**
 * 提示语一律在**所有刷新之后**才写。
 *
 * 为什么不是由 `attempt` 顺手写：写操作之后要重拉列表、还要（在搜索页）重跑一次搜索，
 * 而重跑搜索会清掉提示 —— 于是"已发送请求"刚出现就被自己擦掉。这类"提示被后续刷新
 * 静默清空"的 bug 在界面上表现为"按钮点了没反应"，最难排查（真机验收抓到的）。
 */
async function sendRequest(hit: SearchHit): Promise<void> {
  if (!(await attempt(() => friendsApi.sendRequest(hit.id)))) return
  await loadAll()
  // 搜索结果里的关系状态跟着变（"加好友"→"已发送"），所以重搜一次而不是就地猜。
  await doSearch()
  notice.value = `已向 ${hit.displayName} 发送好友请求`
}

async function accept(req: FriendRequest): Promise<void> {
  if (!(await attempt(() => friendsApi.acceptRequest(req.relationshipId)))) return
  await loadAll()
  notice.value = `已和 ${req.user.displayName} 成为好友`
}

async function reject(req: FriendRequest): Promise<void> {
  if (!(await attempt(() => friendsApi.rejectRequest(req.relationshipId)))) return
  await loadAll()
  notice.value = `已拒绝 ${req.user.displayName} 的请求`
}

function askRemove(entry: FriendEntry): void {
  actionError.value = ''
  notice.value = ''
  pending.value = { kind: 'remove', userId: entry.user.id, displayName: entry.user.displayName, relationshipId: entry.relationshipId }
}

function askBlock(userId: number, displayName: string): void {
  actionError.value = ''
  notice.value = ''
  pending.value = { kind: 'block', userId, displayName, relationshipId: 0 }
}

/** 确认条上点「确认」：按 kind 分派。 */
async function confirmPending(): Promise<void> {
  const p = pending.value
  if (!p) return
  pending.value = null
  if (p.kind === 'remove') {
    if (!(await attempt(() => friendsApi.removeFriend(p.relationshipId)))) return
    await loadAll()
    notice.value = `已删除好友 ${p.displayName}`
    return
  }
  if (!(await attempt(() => friendsApi.blockUser(p.userId)))) return
  await loadAll()
  // 搜索结果的"加好友"按钮状态要跟着改（变成"已拉黑"），否则下一次点只会拿到 403。
  if (section.value === 'find') await doSearch()
  if (blockedLoaded.value) await loadBlocked()
  notice.value = `已拉黑 ${p.displayName}：对方发不进消息，双方在列表里互不可见`
}

/** 解除拉黑（mock 可用；真实实现抛 FeatureUnavailable，按钮在那种情况下不显示）。 */
async function unblock(entry: BlockedEntry): Promise<void> {
  if (!(await attempt(() => friendsApi.unblock(entry.user.id)))) return
  await loadBlocked()
  await loadAll()
  notice.value = `已解除对 ${entry.user.displayName} 的拉黑`
}

// ---------- 生命周期 ----------

onMounted(() => {
  notice.value = ''
  if (auth.isLoggedIn) void loadAll()
})

/**
 * 目录页的两条"晚到的登录态"路径都要接住：
 *
 *  1. **刷新页面 / 新标签页**：`stores/auth` 的启动引导靠 HttpOnly Cookie 恢复会话，
 *     `onMounted` 那一刻 `status` 还是 `restoring`，直接请求会一次 401。
 *  2. **本页点「去登录」回来**：那种情况组件会重新挂载，第 1 条之外不必额外处理。
 *
 * 所以这里盯 `isLoggedIn` 的**翻转**再补一次加载 —— 少了它，"刷新以后好友列表一直是空的"
 * 会成为一个只在刷新时出现的幽灵 bug。
 */
watch(
  () => auth.isLoggedIn,
  (loggedIn) => {
    if (loggedIn && friends.value.length === 0 && requests.value.incoming.length === 0) void loadAll()
  },
)

/** 切到「黑名单」时才去拉它：那一栏在后端可能没有接口，没必要在首屏就撞一次。 */
watch(section, (next) => {
  notice.value = ''
  actionError.value = ''
  pending.value = null
  if (next === 'blocked' && auth.isLoggedIn && !blockedLoaded.value) void loadBlocked()
  if (next === 'find' && auth.isLoggedIn) void probeSearch()
})
</script>

<template>
  <div class="page">
    <main class="body">
      <div class="head">
        <h1 class="title">好友</h1>
        <p class="dim sub">双向确认的关系：请求、接受、删除与拉黑都由服务端判定。</p>
      </div>

      <!-- 恢复登录中：`status==='restoring'` 是"有档案缓存、正在用 Cookie 换 token"，
           把这段时间说成"未登录"会让人以为掉线了。 -->
      <p v-if="auth.restoring" class="dim" role="status">正在恢复登录状态…</p>

      <!-- 未登录：好友是账号内功能，这里只给入口，不自动跳转（跳转由用户主动点）。 -->
      <section v-else-if="!auth.isLoggedIn" class="gate" role="status">
        <h2 class="gate-head">
          <BrandIcon name="users-group" decorative :size="16" />
          登录后才有好友
        </h2>
        <p class="dim">好友关系挂在账号上，游客身份没有可维护的列表。</p>
        <RouterLink class="primary" :to="{ name: 'login', query: { redirect: route.fullPath } }">去登录</RouterLink>
      </section>

      <template v-else>
        <nav class="tabs" role="tablist" aria-label="好友分区">
          <button role="tab" :aria-selected="section === 'friends'" :class="{ on: section === 'friends' }" @click="section = 'friends'">好友</button>
          <button role="tab" :aria-selected="section === 'requests'" :class="{ on: section === 'requests' }" @click="section = 'requests'">
            待处理
            <span v-if="incomingCount > 0" class="badge">{{ incomingCount }}</span>
          </button>
          <button role="tab" :aria-selected="section === 'find'" :class="{ on: section === 'find' }" @click="section = 'find'">找朋友</button>
          <button role="tab" :aria-selected="section === 'blocked'" :class="{ on: section === 'blocked' }" @click="section = 'blocked'">黑名单</button>
        </nav>

        <p v-if="notice" class="ok" role="status">{{ notice }}</p>
        <p v-if="actionError" class="alert" role="alert">{{ actionError }}</p>

        <!-- 危险动作确认条：动作 + 对象 + 后果，写全再让人点。 -->
        <section v-if="pending" class="confirm" role="alertdialog" aria-labelledby="confirm-head">
          <h2 id="confirm-head" class="confirm-head">{{ confirmTitle }}</h2>
          <p class="confirm-text">{{ confirmConsequence }}</p>
          <div class="confirm-acts">
            <button type="button" class="primary" :disabled="actionBusy" @click="confirmPending">确认</button>
            <button type="button" class="btn" :disabled="actionBusy" @click="pending = null">取消</button>
          </div>
        </section>

        <p v-if="busy" class="dim">加载中…</p>
        <CapabilityNotice
          v-else-if="capabilityMissing"
          domain="friends"
          hint="上线后这里是好友列表、待处理请求、按用户名找人，以及黑名单。"
          retryable
          @retry="loadAll"
        />
        <template v-else>
          <p v-if="failureText" class="alert" role="alert">{{ failureText }}</p>

          <!-- ---------- 好友列表 ---------- -->
          <section v-show="section === 'friends'" class="sect">
            <p v-if="friends.length === 0" class="dim">还没有好友。到「找朋友」按用户名加一个人。</p>
            <ul v-else class="rows">
              <li v-for="f in friends" :key="f.relationshipId" class="row">
                <span class="avatar" :style="{ background: f.user.avatarColor }" aria-hidden="true">{{ f.user.avatarChar }}</span>
                <div class="who">
                  <strong>{{ f.user.displayName }}</strong>
                  <span v-if="presenceLabel(f.presence)" class="small dim">{{ presenceLabel(f.presence) }}</span>
                </div>
                <div class="acts">
                  <!-- 跨票契约：utils/dmRoute.ts（票 #30 解析 peer 参数） -->
                  <RouterLink class="btn" :to="conversationPath(f.user.id)">发消息</RouterLink>
                  <button type="button" class="btn" :disabled="actionBusy" @click="askBlock(f.user.id, f.user.displayName)">拉黑</button>
                  <button type="button" class="btn danger" :disabled="actionBusy" @click="askRemove(f)">删除好友</button>
                </div>
              </li>
            </ul>
            <p class="foot dim">
              在线状态要等用户级实时通道（<code>/ws/user</code>）落地：在那之前不显示，而不是显示成「离线」。
            </p>
          </section>

          <!-- ---------- 待处理请求 ---------- -->
          <section v-show="section === 'requests'" class="sect">
            <h2 class="sect-head">收到的请求（{{ requests.incoming.length }}）</h2>
            <p v-if="requests.incoming.length === 0" class="dim">没有待处理的请求。</p>
            <ul v-else class="rows">
              <li v-for="r in requests.incoming" :key="r.relationshipId" class="row">
                <span class="avatar" :style="{ background: r.user.avatarColor }" aria-hidden="true">{{ r.user.avatarChar }}</span>
                <div class="who">
                  <strong>{{ r.user.displayName }}</strong>
                  <span class="small dim">请求加你为好友</span>
                </div>
                <div class="acts">
                  <button type="button" class="primary sm" :disabled="actionBusy" @click="accept(r)">接受</button>
                  <button type="button" class="btn" :disabled="actionBusy" @click="reject(r)">拒绝</button>
                </div>
              </li>
            </ul>

            <h2 class="sect-head">我发出的（{{ requests.outgoing.length }}）</h2>
            <p v-if="requests.outgoing.length === 0" class="dim">没有等待对方处理的请求。</p>
            <ul v-else class="rows">
              <li v-for="r in requests.outgoing" :key="r.relationshipId" class="row">
                <span class="avatar" :style="{ background: r.user.avatarColor }" aria-hidden="true">{{ r.user.avatarChar }}</span>
                <div class="who">
                  <strong>{{ r.user.displayName }}</strong>
                  <span class="small dim">等待对方处理</span>
                </div>
                <div class="acts">
                  <!-- 服务端 6 个端点里没有"撤回请求"：pending 只能由 addressee 处理。
                       这里如实写出来，而不是放一个点了会 404 的按钮。 -->
                  <span class="small dim">不可撤回（需对方处理）</span>
                </div>
              </li>
            </ul>
          </section>

          <!-- ---------- 找朋友 ---------- -->
          <section v-show="section === 'find'" class="sect">
            <form class="find" @submit.prevent="doSearch">
              <input v-model="query" type="search" placeholder="按用户名或昵称搜索，如 miao / 喵喵" aria-label="按用户名搜索" />
              <button type="submit" class="primary" :disabled="query.trim() === ''">搜索</button>
            </form>

            <p v-if="searchUnavailable" class="inline-note" role="status">
              服务端暂未提供「按用户名找人」接口：加好友需要对方的 userId，而 userId 在界面上并不可得。
              这一栏等后端补上搜索接口后再可用；好友列表、请求与拉黑不受影响。
            </p>
            <template v-else>
              <p v-if="!searched" class="dim">输入用户名或昵称开始搜索。</p>
              <p v-else-if="hits.length === 0" class="dim">没找到匹配的人。</p>
              <ul v-else class="rows">
                <li v-for="h in hits" :key="h.id" class="row">
                  <span class="avatar" :style="{ background: h.avatarColor }" aria-hidden="true">{{ h.avatarChar }}</span>
                  <div class="who">
                    <strong>{{ h.displayName }}</strong>
                    <span class="small dim">{{ relationLabel(h.relation) }}</span>
                  </div>
                  <div class="acts">
                    <button
                      type="button"
                      class="primary sm"
                      :disabled="actionBusy || h.relation !== 'none'"
                      @click="sendRequest(h)"
                    >
                      {{ h.relation === 'none' ? '加好友' : '已处理' }}
                    </button>
                    <button
                      type="button"
                      class="btn"
                      :disabled="actionBusy || h.relation === 'blocked'"
                      @click="askBlock(h.id, h.displayName)"
                    >
                      拉黑
                    </button>
                  </div>
                </li>
              </ul>
            </template>
          </section>

          <!-- ---------- 黑名单 ---------- -->
          <section v-show="section === 'blocked'" class="sect">
            <!-- 验收第 2 条：拉黑的后果必须在界面上写清，而不是只留一个移除按钮。 -->
            <div class="explain">
              <h2 class="explain-head">
                <BrandIcon name="alert-triangle" decorative :size="15" />
                拉黑意味着什么
              </h2>
              <ul>
                <li>被拉黑方<strong>发不进消息</strong>（服务端校验关系，不是前端隐藏输入框）。</li>
                <li>被拉黑方发不出好友请求；已有的请求会被这一行覆盖。</li>
                <li>双方在好友列表与请求列表里<strong>互不可见</strong>。</li>
                <li>拉黑是单向的：只有拉黑方能解除，被拉黑方看不到这一行。</li>
              </ul>
            </div>

            <p v-if="blockedUnavailable" class="inline-note" role="status">
              服务端暂未提供「黑名单列表」与「解除拉黑」接口：拉黑已经生效（后果同上），
              但暂时无法在这里回看名单，也无法解除。这是接口缺口，不是权限问题。
            </p>
            <template v-else>
              <p v-if="!blockedLoaded" class="dim">加载中…</p>
              <p v-else-if="blocked.length === 0" class="dim">黑名单是空的。</p>
              <ul v-else class="rows">
                <li v-for="b in blocked" :key="b.user.id" class="row">
                  <span class="avatar" :style="{ background: b.user.avatarColor }" aria-hidden="true">{{ b.user.avatarChar }}</span>
                  <div class="who">
                    <strong>{{ b.user.displayName }}</strong>
                    <span class="small dim">已被你拉黑</span>
                  </div>
                  <div class="acts">
                    <button type="button" class="btn" :disabled="actionBusy" @click="unblock(b)">解除拉黑</button>
                  </div>
                </li>
              </ul>
            </template>
          </section>
        </template>
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

.tabs { display: flex; gap: 2px; flex-wrap: wrap; }
.tabs button { padding: 6px 16px; color: var(--dim); border-bottom: 2px solid transparent; display: inline-flex; align-items: center; gap: 6px; }
.tabs button.on { color: var(--text); border-bottom-color: var(--accent); }
.badge { min-width: 18px; padding: 0 5px; border-radius: 9px; background: var(--accent); color: var(--accent-ink); font-size: 11px; font-weight: 700; }

.sect { display: flex; flex-direction: column; gap: 12px; }
.sect-head { margin: 6px 0 0; font-size: 14px; color: var(--dim); }

/* 行布局：头像 / 主体 / 动作。375 档动作换行到第二行，1280 档单行右对齐。 */
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
.avatar { width: 36px; height: 36px; border-radius: 50%; display: inline-flex; align-items: center; justify-content: center; color: #fff; font-weight: 700; font-size: 15px; }
.who { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.who strong { font-size: 14px; overflow-wrap: anywhere; }
.acts { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }

.btn { padding: 6px 14px; border: 1px solid var(--line); color: var(--dim); font-size: 13px; }
.btn:hover { color: var(--text); border-color: var(--accent); text-decoration: none; }
.btn:disabled { opacity: 0.45; cursor: not-allowed; }
.btn.danger:hover { color: var(--danger); border-color: var(--danger); }
.primary { display: inline-flex; align-items: center; justify-content: center; padding: 8px 18px; background: var(--accent); color: var(--accent-ink); font-weight: 700; font-size: 13px; }
.primary:hover { text-decoration: none; filter: brightness(1.08); }
.primary:disabled { opacity: 0.5; cursor: not-allowed; }
.primary.sm { padding: 6px 14px; }

.find { display: flex; gap: 8px; }
.find input { flex: 1; min-width: 0; padding: 8px 10px; border: 1px solid var(--line); background: var(--bg); color: var(--text); font-size: 13px; }

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
}
</style>
