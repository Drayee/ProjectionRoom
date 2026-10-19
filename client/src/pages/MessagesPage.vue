<script setup lang="ts">
/**
 * 消息（/messages）。wayfinder #30：把占位页换成真页面。
 *
 * 三块：会话列表（按最近消息倒序 + 每对话未读徽标）、聊天窗（历史上拉 + 发送 +
 * 已读上报）、以及"没选会话"时的引导。全部只经由门面 `api/messages.ts`
 * （默认 mock，`VITE_MOCK_MESSAGES=0` 时走真实接口 #31）。
 *
 * 五条纪律，每条都有踩过的出处：
 *
 *  1. **URL 是会话的唯一真相**（跨票契约 `utils/dmRoute.ts`，#28 定、本票消费）。
 *     选中会话 = `router.replace(conversationPath(id))`，而不是只在内存里存一个
 *     `activePeer`。好处有三：刷新/新标签页能回到同一个对话；`/friends` 的「发消息」
 *     直接可用；浏览器后退键符合直觉。所以这里**没有** `selectPeer()` 这种只改内存的入口。
 *  2. **授权 100% 在服务端**（铁律 I3）。本页不判断"我能不能给这个人发消息"：
 *     照常发，拿 403 就照服务端的文案原地说明（`MESSAGE_BLOCKED` = 存在拉黑关系、
 *     `MESSAGE_NOT_FRIENDS` = 只能给好友发）。按钮显隐只是省一次注定失败的往返。
 *  3. **已读上报用"当前最大 id"**，且**只报收到的**。服务端 `upTo` 缺省不代表全部已读
 *     （会 400），所以必须给一个具体的 id；而把我自己发出的消息算进 `upTo` 是无害的
 *     （服务端只动 `recipient_id = 我` 的行），但会让"读到哪"这个语义变得含糊 ——
 *     这里取"收到的最后一条"更准确。
 *  4. **历史顺序在页面里倒一次**。服务端与 mock 都返回 id DESC（最新在前），
 *     聊天气泡要从上往下读，所以渲染前 `reverse()`。**不在适配层倒**：`before`
 *     游标要的是"当前最旧那条的 id"，顺序只在这一处定义才不会两边打架。
 *  5. **未读数只有一个写入点**（`stores/auth.ts` 的 `setUnread`）。顶栏徽标、会话徽标、
 *     "读完清零"读的是同一个数字；本页读完一屏后写它，并顺手把本地会话行的
 *     `unreadCount` 清零 —— 不等下一次整页加载，否则"读完了徽标还在"。
 */
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import * as messagesApi from '../api/messages'
import { MAX_MESSAGE_BODY } from '../api/messages'
import { useApiFailure } from '../components/features/common/useApiFailure'
import { useAuthStore } from '../stores/auth'
import { conversationPath, parseConversationPeer } from '../utils/dmRoute'
import type { ConversationEntry, DirectMessage } from '../api/messages'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { failure, run } = useApiFailure(() => route.fullPath)

/** 会话列表（服务端派生视图：每个对话一行，按最近消息倒序）。 */
const conversations = ref<ConversationEntry[]>([])
/**
 * 聊天窗里的消息，**按时间正序**（旧 → 新），渲染时直接从上往下铺。
 * 从门面拿到的是 id DESC，装载时倒一次（见文件头第 4 条）。
 */
const thread = ref<DirectMessage[]>([])
const hasMore = ref(false)

const loadingList = ref(false)
const loadingThread = ref(false)
const loadingMore = ref(false)
const sending = ref(false)

/** 整域未上线（翻成真实实现而后端连不上/没注册）。 */
const capabilityMissing = ref(false)

/** 成功提示（一次性，下一次动作就清）。 */
const notice = ref('')
/** 写操作失败文案（服务端原样，原地说明）。 */
const actionError = ref('')

const draft = ref('')
/** 聊天窗滚动容器：发完消息要贴到底部。 */
const scroller = ref<HTMLElement | null>(null)

/** 当前会话的对方 id（来自 URL；非法值一律 null，见 `parseConversationPeer`）。 */
const activePeer = computed(() => parseConversationPeer(route.query.peer))

/** 当前会话在列表里的那一行（拿对方的显示名与未读数）。 */
const activeEntry = computed(() => {
  const id = activePeer.value
  if (id === null) return null
  return conversations.value.find((c) => c.peer.id === id) ?? null
})

/** 聊天窗标题：列表里没有这个人时（还没聊过），显示 id 而不是编一个名字。 */
const activeTitle = computed(() => {
  if (activePeer.value === null) return ''
  return activeEntry.value?.peer.displayName ?? `用户 ${activePeer.value}`
})

/** 全部未读之和（会话列表的徽标合计，与 `/me` 的 unread 应一致）。 */
const totalUnread = computed(() => conversations.value.reduce((sum, c) => sum + c.unreadCount, 0))

/** 失败态的一行文案（分诊结果，见 useApiFailure）。 */
const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能查看消息'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

/** 草稿长度（按 rune 数算，与 `MAX_MESSAGE_BODY` 同一口径：中文一个字算一个）。 */
const draftLength = computed(() => [...draft.value.trim()].length)
const draftTooLong = computed(() => draftLength.value > MAX_MESSAGE_BODY)
const canSend = computed(
  () => activePeer.value !== null && draftLength.value > 0 && !draftTooLong.value && !sending.value,
)

/** 时间显示：今天只给时分，更早给月日。非法时间原样透出（不编一个假时间）。 */
function shortTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const now = new Date()
  const sameDay =
    d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate()
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  if (sameDay) return `${hh}:${mm}`
  return `${d.getMonth() + 1}/${d.getDate()} ${hh}:${mm}`
}

/**
 * 把未读数写回两处：本页的会话行 + `stores/auth.ts`（顶栏徽标读它）。
 *
 * 为什么两处都要：会话行的 `unreadCount` 是服务端派生的**快照**，不清它就会出现
 * "点进会话读完、退出来徽标还在"；而顶栏徽标读的是 store。同一个事实必须一次写完。
 */
function clearUnread(peerId: number, remaining: number): void {
  const row = conversations.value.find((c) => c.peer.id === peerId)
  if (row) row.unreadCount = Math.max(0, remaining)
  auth.setUnread(conversations.value.reduce((sum, c) => sum + c.unreadCount, 0))
}

/** 会话列表（首屏，同时是域探针：整域 404 由它决定整页降级）。 */
async function loadConversations(): Promise<void> {
  if (!auth.isLoggedIn) return
  loadingList.value = true
  const list = await run(() => messagesApi.listConversations())
  loadingList.value = false
  if (list === null) {
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      conversations.value = []
      thread.value = []
    }
    return
  }
  capabilityMissing.value = false
  conversations.value = list
  // 会话列表是未读数的权威来源之一（另一处是 `/me`）：拿到就把总数同步给 store，
  // 这样"打开消息页"本身就会纠正顶栏那个可能过期的数字。
  auth.setUnread(list.reduce((sum, c) => sum + c.unreadCount, 0))
}

/**
 * 拉一页历史并装载。
 *
 * `reset=true` 换会话时重来（清空 + 从第一页拉）；`false` 时往上翻更早的
 * （`before` = 当前最旧那条的 id），把新拉到的拼在**前面**。
 */
async function loadThread(reset: boolean): Promise<void> {
  const peerId = activePeer.value
  if (peerId === null || !auth.isLoggedIn) {
    thread.value = []
    hasMore.value = false
    return
  }

  if (reset) {
    loadingThread.value = true
  } else {
    if (loadingMore.value || !hasMore.value) return
    loadingMore.value = true
  }

  const before = reset ? undefined : thread.value[0]?.id
  const page = await run(() => messagesApi.listHistory({ peerId, before }))

  if (reset) loadingThread.value = false
  else loadingMore.value = false

  if (page === null) {
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      thread.value = []
    }
    return
  }

  // 服务端/ mock 都是 id DESC（最新在前）→ 倒一次变成"从上往下读"的顺序。
  const incoming = [...page.items].reverse()
  if (reset) {
    thread.value = incoming
    hasMore.value = page.hasMore
    await scrollToBottom()
  } else {
    thread.value = [...incoming, ...thread.value]
    hasMore.value = page.hasMore
  }

  // 换会话（或首屏）时上报已读：只在 reset 做，往上翻历史不该再触发一次写。
  if (reset) await reportRead(peerId)
}

/**
 * 上报"读到哪一条"。
 *
 * 只报**收到的**最后一条（`senderId !== 我`）：把我自己发的算进 `upTo` 虽然无害
 * （服务端只动 `recipient_id = 我` 的行），但会让"读到哪"变得含糊。
 * 没有收到的消息就不报 —— 一次注定 `updated = 0` 的写请求只是噪音。
 */
async function reportRead(peerId: number): Promise<void> {
  const lastIncoming = [...thread.value].reverse().find((m) => !m.mine)
  if (!lastIncoming) {
    clearUnread(peerId, 0)
    return
  }
  const updated = await run(() => messagesApi.markRead({ peerId, upTo: lastIncoming.id }))
  if (updated === null) return
  if (updated > 0) {
    const row = conversations.value.find((c) => c.peer.id === peerId)
    // 服务端返回的是"本次真正标记的行数"：用它减去本地记录的未读，而不是拍 0 ——
    // 往上翻历史时可能有更早的未读还没进本页，扣多了会让数字凭空消失。
    clearUnread(peerId, Math.max(0, (row?.unreadCount ?? 0) - updated))
  }
}

/** 把聊天窗滚到底（发完消息 / 换会话时）。 */
async function scrollToBottom(): Promise<void> {
  await nextTick()
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

/** 选中会话 = 改 URL（见文件头第 1 条）。 */
function openConversation(peerId: number): void {
  notice.value = ''
  actionError.value = ''
  if (activePeer.value === peerId) return
  void router.replace(conversationPath(peerId))
}

/**
 * 发送。
 *
 * 顺序很重要：先本地拦超长/空（省一次注定 400 的往返），再发；成功后**用服务端返回的
 * 权威行** append（不是本地编一条），然后清草稿、滚到底、重拉会话列表把顺序与摘要刷新。
 *
 * 失败**不动草稿**：用户刚打的字必须还在输入框里，否则"被拉黑了"这件事的代价是
 * 他要重打一遍。
 */
async function doSend(): Promise<void> {
  const peerId = activePeer.value
  if (peerId === null || !canSend.value) return
  actionError.value = ''
  notice.value = ''

  const body = draft.value.trim()
  sending.value = true
  const sent = await run(() => messagesApi.send({ recipientId: peerId, body }))
  sending.value = false
  if (sent === null) {
    const f = failure.value
    if (f?.kind === 'capability') {
      capabilityMissing.value = true
      return
    }
    if (f && (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error')) {
      actionError.value = f.message
    }
    return
  }

  thread.value = [...thread.value, sent]
  draft.value = ''
  await scrollToBottom()
  // 会话列表要跟着变（这条消息成了该对话的"最近一条"，顺序与摘要都变了）。
  // 放在最后：`loadConversations` 会重算未读总数，先写提示会被它盖掉（#28 的教训）。
  await loadConversations()
  notice.value = '已发送'
}

// ---------- 生命周期 ----------

onMounted(() => {
  notice.value = ''
  if (auth.isLoggedIn) void loadConversations()
})

/**
 * 两条"晚到的登录态"路径（与 `/friends` 同一条理由）：
 *  1. 刷新页面 / 新标签页：`onMounted` 那一刻 `status` 还是 `restoring`，直接请求会 401；
 *  2. 从登录页回来：组件重新挂载，第 1 条之外不必额外处理。
 * 少了它，"刷新以后消息一直是空的"会成为一个只在刷新时出现的幽灵 bug。
 */
watch(
  () => auth.isLoggedIn,
  (loggedIn) => {
    if (loggedIn && conversations.value.length === 0) void loadConversations()
  },
)

/** 换会话（URL 变了）：重拉历史。`immediate` 让首屏也走同一条路。 */
watch(
  activePeer,
  () => {
    if (!auth.isLoggedIn) return
    notice.value = ''
    actionError.value = ''
    draft.value = ''
    void loadThread(true)
  },
  { immediate: true },
)
</script>

<template>
  <div class="page">
    <main class="body">
      <div class="head">
        <h1 class="title">消息</h1>
        <p class="dim sub">私聊只在好友之间（服务端校验关系与拉黑），消息落库、未读可追。</p>
      </div>

      <!-- 恢复登录中：说成"未登录"会让人以为掉线了。 -->
      <p v-if="auth.restoring" class="dim" role="status">正在恢复登录状态…</p>

      <!-- 未登录：私聊挂在账号上，这里只给入口，不自动跳转。 -->
      <section v-else-if="!auth.isLoggedIn" class="gate" role="status">
        <h2 class="gate-head">
          <BrandIcon name="msg" decorative :size="16" />
          登录后才有消息
        </h2>
        <p class="dim">私聊的可见性取决于你和对方的关系，游客身份没有可读的会话。</p>
        <RouterLink class="primary" :to="{ name: 'login', query: { redirect: route.fullPath } }">去登录</RouterLink>
      </section>

      <template v-else>
        <p v-if="notice" class="ok" role="status">{{ notice }}</p>
        <p v-if="actionError" class="alert" role="alert">{{ actionError }}</p>

        <p v-if="loadingList && conversations.length === 0" class="dim">加载中…</p>
        <CapabilityNotice
          v-else-if="capabilityMissing"
          domain="messages"
          hint="上线后这里是会话列表、未读徽标、历史分页与发送。"
          retryable
          @retry="loadConversations"
        />
        <template v-else>
          <p v-if="failureText" class="alert" role="alert">{{ failureText }}</p>

          <!-- 两栏：1280 档列表与聊天窗并排；375 档上下叠（见 <style> 的媒体查询）。 -->
          <div class="split">
            <!-- ---------- 会话列表 ---------- -->
            <aside class="list" aria-label="会话列表">
              <div class="list-head">
                <span class="list-title">会话</span>
                <!-- 未读数：服务端明确返回过才显示数字，否则说"不可知"。
                     0 与"不知道"在界面上必须可分辨（同 AuthUser.emailVerified 的纪律）。 -->
                <span v-if="auth.unread === null" class="small dim">未读数：服务端未返回</span>
                <span v-else-if="auth.unread > 0" class="badge">{{ auth.unread }} 未读</span>
                <span v-else class="small dim">没有未读</span>
              </div>

              <p v-if="conversations.length === 0" class="dim empty">
                还没有任何会话。到「好友」里找一个人，点「发消息」就能开始。
              </p>
              <ul v-else class="rows">
                <li v-for="c in conversations" :key="c.peer.id">
                  <button
                    type="button"
                    class="conv"
                    :class="{ on: c.peer.id === activePeer }"
                    :aria-current="c.peer.id === activePeer ? 'true' : undefined"
                    @click="openConversation(c.peer.id)"
                  >
                    <span class="avatar" :style="{ background: c.peer.avatarColor }" aria-hidden="true">
                      {{ c.peer.avatarChar }}
                    </span>
                    <span class="conv-main">
                      <span class="conv-top">
                        <strong class="conv-name">{{ c.peer.displayName }}</strong>
                        <span class="small dim">{{ shortTime(c.lastSentAt) }}</span>
                      </span>
                      <span class="conv-preview dim">{{ c.lastBody }}</span>
                    </span>
                    <span v-if="c.unreadCount > 0" class="badge" :aria-label="`${c.unreadCount} 条未读`">
                      {{ c.unreadCount }}
                    </span>
                  </button>
                </li>
              </ul>
            </aside>

            <!-- ---------- 聊天窗 ---------- -->
            <section class="thread" aria-label="聊天窗">
              <!-- 没选会话：引导，不是错误。 -->
              <div v-if="activePeer === null" class="thread-empty" role="status">
                <BrandIcon name="msg" decorative :size="22" />
                <p>从左边选一个会话。</p>
                <p class="small dim">
                  也可以从
                  <RouterLink class="inline-link" :to="{ name: 'friends' }">好友</RouterLink>
                  点「发消息」直接进来。
                </p>
              </div>

              <template v-else>
                <header class="thread-head">
                  <span
                    v-if="activeEntry"
                    class="avatar"
                    :style="{ background: activeEntry.peer.avatarColor }"
                    aria-hidden="true"
                  >
                    {{ activeEntry.peer.avatarChar }}
                  </span>
                  <div class="thread-who">
                    <strong>{{ activeTitle }}</strong>
                    <!-- 还没聊过时列表里没有这个人：如实说"这是新会话"，
                         而不是假装它是一个已有对话。 -->
                    <span v-if="!activeEntry" class="small dim">新会话（还没有消息）</span>
                  </div>
                  <RouterLink
                    v-if="activePeer !== null"
                    class="btn"
                    :to="{ name: 'user', params: { id: String(activePeer) } }"
                  >
                    看主页
                  </RouterLink>
                </header>

                <p v-if="loadingThread" class="dim pad">加载中…</p>

                <div v-else ref="scroller" class="scroller">
                  <div class="more">
                    <button v-if="hasMore" type="button" class="btn" :disabled="loadingMore" @click="loadThread(false)">
                      {{ loadingMore ? '加载中…' : '往上翻更早的消息' }}
                    </button>
                    <p v-else-if="thread.length > 0" class="small dim">已经到最早一条了。</p>
                  </div>

                  <!-- 空会话：验收第 2 条的边界之一。 -->
                  <p v-if="thread.length === 0" class="dim empty-thread">
                    你们还没有聊过。发第一条吧 —— 只能给好友发，对方拉黑你时服务端会拒。
                  </p>
                  <ul v-else class="bubbles">
                    <li v-for="m in thread" :key="m.id" class="bubble-row" :class="{ mine: m.mine }">
                      <span
                        v-if="!m.mine"
                        class="avatar sm"
                        :style="{ background: m.sender.avatarColor }"
                        aria-hidden="true"
                      >
                        {{ m.sender.avatarChar }}
                      </span>
                      <div class="bubble-wrap">
                        <p class="bubble">{{ m.body }}</p>
                        <p class="small dim bubble-meta">
                          {{ shortTime(m.createdAt) }}
                          <span v-if="m.mine">{{ m.readAt ? '· 已读' : '· 未读' }}</span>
                        </p>
                      </div>
                    </li>
                  </ul>
                </div>

                <!-- 发送区。超长在本地就拦（`MAX_MESSAGE_BODY` 与服务端同值），
                     而不是让用户按下去等一次 400。 -->
                <form class="composer" @submit.prevent="doSend">
                  <textarea
                    v-model="draft"
                    class="input"
                    rows="2"
                    :placeholder="`发给 ${activeTitle}…（Enter 发送，Shift+Enter 换行）`"
                    aria-label="消息内容"
                    @keydown.enter.exact.prevent="doSend"
                  />
                  <div class="composer-side">
                    <span class="small" :class="draftTooLong ? 'over' : 'dim'">
                      {{ draftLength }} / {{ MAX_MESSAGE_BODY }}
                    </span>
                    <button type="submit" class="primary" :disabled="!canSend">发送</button>
                  </div>
                </form>
                <p v-if="draftTooLong" class="alert" role="alert">
                  消息内容不能超过 {{ MAX_MESSAGE_BODY }} 个字符（服务端同一上限，超出会被拒）。
                </p>
              </template>
            </section>
          </div>
        </template>
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
  width: min(1080px, 100%);
  margin: 0 auto;
  padding: 32px 20px 48px;
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

.gate {
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  align-items: flex-start;
}
.gate-head {
  margin: 0;
  font-size: 15px;
  display: flex;
  align-items: center;
  gap: 6px;
}

/* 两栏：列表定宽 300，聊天窗吃满剩余。375 档改成上下叠（列表在上）。 */
.split {
  display: grid;
  grid-template-columns: 300px minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}

.list {
  border: 1px solid var(--line);
  background: var(--surface);
  display: flex;
  flex-direction: column;
  min-height: 0;
}
.list-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
}
.list-title {
  font-size: 13px;
  font-weight: 700;
}

.rows {
  list-style: none;
  margin: 0;
  padding: 6px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 62vh;
  overflow-y: auto;
}
.conv {
  width: 100%;
  display: grid;
  grid-template-columns: 32px minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid transparent;
  text-align: left;
  color: var(--text);
}
.conv:hover {
  border-color: var(--line);
  background: rgba(180, 108, 255, 0.06);
}
.conv.on {
  border-color: var(--accent);
  background: rgba(180, 108, 255, 0.1);
}
.conv-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.conv-top {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}
.conv-name {
  font-size: 13px;
  overflow-wrap: anywhere;
}
.conv-preview {
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-weight: 700;
  font-size: 14px;
  flex: none;
}
.avatar.sm {
  width: 26px;
  height: 26px;
  font-size: 12px;
}

.thread {
  border: 1px solid var(--line);
  background: var(--surface);
  display: flex;
  flex-direction: column;
  min-height: 480px;
}
.thread-empty {
  margin: auto;
  padding: 48px 20px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  color: var(--dim);
  text-align: center;
}
.thread-empty p {
  margin: 0;
  font-size: 13px;
}
.thread-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
}
.thread-who {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.thread-who strong {
  font-size: 14px;
  overflow-wrap: anywhere;
}

.scroller {
  flex: 1;
  min-height: 320px;
  max-height: 56vh;
  overflow-y: auto;
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.more {
  display: flex;
  justify-content: center;
}
.more p {
  margin: 0;
}

.bubbles {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.bubble-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  justify-content: flex-start;
}
.bubble-row.mine {
  justify-content: flex-end;
}
.bubble-wrap {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-width: min(70%, 520px);
  min-width: 0;
}
.bubble-row.mine .bubble-wrap {
  align-items: flex-end;
}
.bubble {
  margin: 0;
  padding: 8px 12px;
  border: 1px solid var(--line);
  background: var(--bg);
  font-size: 13px;
  line-height: 1.6;
  /* 正文是纯文本（`{{ }}` 插值）：换行靠 pre-wrap，不拼 HTML。 */
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.bubble-row.mine .bubble {
  border-color: var(--accent);
  background: rgba(180, 108, 255, 0.12);
}
.bubble-meta {
  margin: 0;
}

.composer {
  border-top: 1px solid var(--line);
  padding: 10px 14px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.input {
  width: 100%;
  resize: vertical;
  padding: 8px 10px;
  border: 1px solid var(--line);
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
  font-family: inherit;
  line-height: 1.6;
}
.composer-side {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
}

.btn {
  padding: 6px 14px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
  text-decoration: none;
}
.btn:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}
.btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.primary {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 8px 18px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
  font-size: 13px;
}
.primary:hover {
  text-decoration: none;
  filter: brightness(1.08);
}
.primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.badge {
  min-width: 18px;
  padding: 0 6px;
  border-radius: 9px;
  background: var(--accent);
  color: var(--accent-ink);
  font-size: 11px;
  font-weight: 700;
  text-align: center;
}

.empty {
  margin: 0;
  padding: 14px;
  font-size: 13px;
  line-height: 1.6;
}
.empty-thread {
  margin: auto;
  padding: 24px 16px;
  font-size: 13px;
  line-height: 1.7;
  text-align: center;
}
.pad {
  padding: 14px;
}

.inline-link {
  color: var(--accent);
}
.over {
  color: var(--danger);
}
.ok {
  margin: 0;
  padding: 8px 10px;
  border: 1px solid var(--accent);
  background: rgba(180, 108, 255, 0.1);
  font-size: 12px;
}
.alert {
  margin: 0;
  padding: 8px 10px;
  border: 1px solid var(--danger);
  background: rgba(255, 92, 92, 0.12);
  font-size: 12px;
  color: #ffdada;
}
.small {
  font-size: 12px;
}

/* 375 档：列表在上、聊天窗在下；列表不再吃掉半屏高度。 */
@media (max-width: 760px) {
  .split {
    grid-template-columns: minmax(0, 1fr);
  }
  .rows {
    max-height: 30vh;
  }
  .scroller {
    max-height: 46vh;
  }
  .bubble-wrap {
    max-width: 82%;
  }
}
</style>
