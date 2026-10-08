<script setup lang="ts">
/**
 * 管理端（`/admin`）。
 *
 * 这一页的每一个结论都来自**服务端**：
 *   - 能不能进：不是本地缓存里的 role 说了算，而是第一次请求的响应说了算。
 *     非 admin 打开这一页会看到「无权限」+ 返回首页，**不跳登录页**（登录是好的，缺的是权限）；
 *   - 用户/房间/日志/审计的内容与分页：全部来自服务端，界面不做二次过滤；
 *   - 危险操作（封禁/解封、改角色、强关、下架）**一律二次确认**，确认框里写清后果，
 *     成功后重新拉列表并给一句成功反馈，失败原样显示服务端文案。
 *
 * 渲染纪律：日志行、标题、昵称、审计明细**全部文本插值**。这些内容里都有用户可控文本
 * （昵称、房间标题、日志里的路径），任何 `v-html`/`innerHTML` 都会把它们变成注入面。
 * 审计的 `detail` 服务端刻意以**文本**下发（jsonb 原文），这里也照文本渲染，不解析成结构。
 */

import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
import { ADMIN_PAGE_SIZE, useAdminStore, type AdminSection } from '../stores/admin'
import type { AdminRoom, AdminUser } from '../api/admin'

const store = useAdminStore()

/** 标签页：图标 + 文字（这几个词是"区块名"而不是主行动按钮文案，保留文字更好定位）。 */
const TABS: Array<{ key: AdminSection; text: string; icon: string }> = [
  { key: 'users', text: '用户', icon: 'users-group' },
  { key: 'rooms', text: '房间', icon: 'video' },
  { key: 'metrics', text: '指标', icon: 'temperature' },
  { key: 'logs', text: '日志', icon: 'chat' },
  { key: 'audit', text: '审计', icon: 'shield-done' },
]

/**
 * 待确认的危险操作。
 *
 * 用**一个**状态描述"现在要确认的是谁、做什么"，而不是每个行一个布尔：
 * 后者在列表刷新之后会与行脱钩（确认框还开着，但那一行已经没了）。
 * 确认动作本身就是一次状态机转移（idle → confirm → 执行 → idle）。
 */
type PendingAction = { kind: 'ban' | 'unban' | 'role' | 'close' | 'unpublish'; key: string; label: string; detail: string }
const pending = ref<PendingAction | null>(null)
/** 改角色时需要选目标角色，因此单独一个下拉值。 */
const roleTarget = ref('user')
/** 封禁原因（进审计明细，可留空）。 */
const reason = ref('')

const activeTab = computed(() => TABS.find((tab) => tab.key === store.section) ?? TABS[0]!)

onMounted(() => {
  void store.bootstrap()
})

function ask(action: PendingAction) {
  reason.value = ''
  roleTarget.value = 'user'
  pending.value = action
}

function cancel() {
  pending.value = null
}

/**
 * 确认并执行。
 *
 * `busyKey` 由 store 维护（按对象分键）：只有正在被操作的那一行会禁用，
 * 而且**确认期间不重复提交**（重复点击不会打出第二个请求）。
 */
async function confirm() {
  const action = pending.value
  if (!action) return
  const user = store.users.items.find((u) => `user:${u.id}` === action.key)
  const room = store.rooms.items.find((r) => `room:${r.roomId}` === action.key)
  if ((action.kind === 'ban' || action.kind === 'unban' || action.kind === 'role') && !user) {
    pending.value = null
    return
  }
  if ((action.kind === 'close' || action.kind === 'unpublish') && !room) {
    pending.value = null
    return
  }

  if (action.kind === 'ban') await store.setUserStatus(user as AdminUser, 'banned', reason.value.trim())
  else if (action.kind === 'unban') await store.setUserStatus(user as AdminUser, 'active', reason.value.trim())
  else if (action.kind === 'role') await store.setUserRole(user as AdminUser, roleTarget.value, reason.value.trim())
  else if (action.kind === 'close') await store.closeRoom(room as AdminRoom)
  else await store.unpublishRoom(room as AdminRoom)

  pending.value = null
}

function askBan(user: AdminUser) {
  ask({
    kind: user.status === 'banned' ? 'unban' : 'ban',
    key: `user:${user.id}`,
    label: `${user.status === 'banned' ? '解封' : '封禁'} ${user.username}`,
    detail:
      user.status === 'banned'
        ? '解封后该账号可以重新登录。'
        : '封禁会立刻生效：该账号的 access token 立即失效，并且**在线的连接会被服务端断开**（房间里的人会掉线）。',
  })
}

function askRole(user: AdminUser) {
  ask({
    kind: 'role',
    key: `user:${user.id}`,
    label: `修改 ${user.username} 的角色`,
    detail: `当前角色：${user.role}。管理员可以管理全部用户、房间、日志与审计，请谨慎授予。`,
  })
}

function askClose(room: AdminRoom) {
  ask({
    kind: 'close',
    key: `room:${room.roomId}`,
    label: `强制关闭房间 ${room.roomId}`,
    detail:
      room.memberCount > 0
        ? `房间里有 ${room.memberCount} 人，强制关闭会**立刻断开房内全部连接**，主播需要重新建房。此操作不可撤销。`
        : '房间当前没有人在座，强制关闭后该房间码立即失效。此操作不可撤销。',
  })
}

function askUnpublish(room: AdminRoom) {
  ask({
    kind: 'unpublish',
    key: `room:${room.roomId}`,
    label: `下架房间 ${room.roomId}`,
    detail: room.isPublic
      ? '下架只影响公开性：房间本身照常可用，但会从公开房间列表消失（不会再出现在 /rooms）。'
      : '该房间本来就没有公开（幂等操作，不会产生额外变更）。',
  })
}

/** 时间戳：只显示到秒（管理端要看"先后顺序"，不需要毫秒）。 */
function when(value: string): string {
  if (value.trim() === '') return '-'
  const t = new Date(value)
  if (Number.isNaN(t.getTime())) return value
  return t.toLocaleString('zh-CN', { hour12: false })
}

/** 房间行是否正在被操作（按钮禁用 + 进行中态）。 */
function roomBusy(room: AdminRoom): boolean {
  return store.busyKey === `room:${room.roomId}:close` || store.busyKey === `room:${room.roomId}:unpublish`
}

function userBusy(user: AdminUser): boolean {
  return store.busyKey.startsWith(`user:${user.id}:`)
}
</script>

<template>
  <div class="admin">
    <AppHeader variant="page" />

    <!-- 整页级拒绝：无权限 / 未登录 / 管理端未启用。三种结论的恢复路径不同，
         所以不共用一句话（把人送去登录页只在"会话真的没了"时才是对的动作）。 -->
    <section class="card denied" v-if="store.isDenied" data-testid="admin-denied">
      <h1>
        <BrandIcon name="lock" label="无权限" :size="20" />
        {{ store.denied === 'forbidden' ? '无权限' : store.denied === 'unauthenticated' ? '需要登录' : '管理端不可用' }}
      </h1>
      <p class="muted">
        {{ store.deniedText }}
      </p>
      <p class="muted small">
        <template v-if="store.denied === 'forbidden'">
          管理端只能由管理员账号访问：接口层由服务端的权限闸门判定，与前端显示无关。
        </template>
        <template v-else-if="store.denied === 'unauthenticated'">
          当前会话已失效，重新登录后再试。若你本来就不是管理员，登录也不会打开这一页。
        </template>
        <template v-else>
          服务端的账号 / 管理能力没有装配完成（少配了数据库连接串时会这样）。这是服务端配置问题。
        </template>
      </p>
      <div class="actions">
        <RouterLink class="back" to="/" data-testid="admin-back-home">
          <BrandIcon name="close" decorative :size="14" />
          返回首页
        </RouterLink>
        <RouterLink
          v-if="store.denied === 'unauthenticated'"
          class="back"
          :to="{ name: 'login', query: { redirect: '/admin' } }"
        >
          <BrandIcon name="login" decorative :size="14" />
          去登录
        </RouterLink>
      </div>
    </section>

    <template v-else>
      <header class="admin-head">
        <h1>
          <BrandIcon name="settings" label="管理端" :size="20" />
          管理端
        </h1>
        <p class="muted">用户、房间、运行指标、服务端日志与审计。所有写操作都会落审计。</p>
      </header>

      <div class="tabs" role="tablist" aria-label="管理端区块">
        <button
          v-for="tab in TABS"
          :key="tab.key"
          type="button"
          role="tab"
          class="tab"
          :class="{ active: store.section === tab.key }"
          :aria-selected="store.section === tab.key"
          :data-testid="`admin-tab-${tab.key}`"
          :disabled="store.booting"
          @click="store.selectSection(tab.key)"
        >
          <BrandIcon :name="tab.icon" decorative :size="14" />
          {{ tab.text }}
        </button>
      </div>

      <!-- 危险操作的成功/失败反馈：紧贴标签页下方，两条互斥。 -->
      <p class="ok-bar" v-if="store.notice" data-testid="admin-notice">
        <BrandIcon name="check-circle" label="成功" :size="15" />
        <span>{{ store.notice }}</span>
      </p>
      <div class="error-bar" v-if="store.noticeError" data-testid="admin-action-error">
        <BrandIcon name="alert-triangle" label="操作失败" :size="15" />
        <span class="msg">{{ store.noticeError }}</span>
        <button type="button" class="icon-btn" aria-label="关闭错误提示" title="关闭错误提示" @click="store.noticeError = ''">
          <BrandIcon name="close" decorative :size="14" />
        </button>
      </div>

      <!-- 二次确认：不用 window.confirm（它没有可访问名、无法承载结构化信息，
           也无法在窄屏上优雅排版）；这一块自己就是确认框，且带 aria-live 让读屏知道它出现了。 -->
      <section class="card confirm" v-if="pending" aria-live="polite" data-testid="admin-confirm">
        <h2>
          <BrandIcon name="alert-triangle" label="危险操作" :size="16" />
          {{ pending.label }}
        </h2>
        <p class="muted small">{{ pending.detail }}</p>
        <label v-if="pending.kind === 'role'" for="admin-role-target">目标角色</label>
        <select v-if="pending.kind === 'role'" id="admin-role-target" v-model="roleTarget">
          <option value="user">user</option>
          <option value="admin">admin</option>
        </select>
        <label v-if="pending.kind === 'ban' || pending.kind === 'unban' || pending.kind === 'role'" for="admin-reason">
          原因（会写进审计，可留空）
        </label>
        <input
          v-if="pending.kind === 'ban' || pending.kind === 'unban' || pending.kind === 'role'"
          id="admin-reason"
          v-model="reason"
          maxlength="120"
          placeholder="例如：发布违规内容"
        />
        <div class="actions">
          <button
            class="danger"
            type="button"
            :disabled="store.busyKey !== ''"
            :aria-busy="store.busyKey !== ''"
            data-testid="admin-confirm-yes"
            @click="confirm()"
          >
            <BrandIcon name="check-circle" decorative :size="14" />
            {{ store.busyKey !== '' ? '执行中…' : '确认执行' }}
          </button>
          <button type="button" :disabled="store.busyKey !== ''" data-testid="admin-confirm-no" @click="cancel()">
            <BrandIcon name="close" decorative :size="14" />
            取消
          </button>
        </div>
      </section>

      <!-- 首次加载：整页骨架。 -->
      <p class="muted loading" v-if="store.booting" aria-busy="true" data-testid="admin-loading">加载中…</p>

      <template v-else>
        <!-- ============ 用户 ============ -->
        <section class="card" v-if="store.section === 'users'" data-testid="admin-users">
          <header class="sec-head">
            <h2><BrandIcon name="users-group" decorative :size="15" />用户</h2>
            <form class="search" @submit.prevent="store.loadUsers(0)">
              <label class="sr-only" for="admin-user-search">按用户名搜索</label>
              <input
                id="admin-user-search"
                v-model="store.userSearch"
                placeholder="搜索用户名"
                data-testid="admin-user-search"
              />
              <button type="submit" :disabled="store.users.loading" :aria-busy="store.users.loading">
                <BrandIcon name="tips" decorative :size="14" />
                搜索
              </button>
            </form>
          </header>

          <div class="error-bar" v-if="store.users.error">
            <BrandIcon name="alert-triangle" label="加载失败" :size="15" />
            <span class="msg">{{ store.users.error }}</span>
            <button type="button" class="icon-btn" aria-label="重试" title="重试" @click="store.loadUsers(store.users.offset)">
              <BrandIcon name="refresh" decorative :size="15" />
            </button>
          </div>

          <p class="muted" v-if="store.users.loading && store.users.items.length === 0">加载中…</p>
          <p class="muted" v-else-if="store.users.items.length === 0">没有匹配的用户。</p>

          <div class="table-wrap" v-else>
            <table class="mono-table">
              <thead>
                <tr>
                  <th>用户名</th>
                  <th>昵称</th>
                  <th>角色</th>
                  <th>状态</th>
                  <th>最近活跃</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="user in store.users.items" :key="user.id" :data-testid="`admin-user-row-${user.id}`">
                  <td>{{ user.username }}</td>
                  <td>{{ user.displayName }}</td>
                  <td><span class="badge" :class="{ host: user.role === 'admin' }">{{ user.role }}</span></td>
                  <td>
                    <span class="badge" :class="user.status === 'banned' ? 'danger' : 'ok'">{{ user.status }}</span>
                  </td>
                  <td class="muted">{{ when(user.lastSeenAt) }}</td>
                  <td class="ops">
                    <button
                      type="button"
                      :disabled="userBusy(user)"
                      :data-testid="`admin-user-${user.status === 'banned' ? 'unban' : 'ban'}-${user.id}`"
                      @click="askBan(user)"
                    >
                      <BrandIcon :name="user.status === 'banned' ? 'shield-done' : 'lock'" decorative :size="13" />
                      {{ user.status === 'banned' ? '解封' : '封禁' }}
                    </button>
                    <button
                      type="button"
                      :disabled="userBusy(user)"
                      :data-testid="`admin-user-role-${user.id}`"
                      @click="askRole(user)"
                    >
                      <BrandIcon name="settings" decorative :size="13" />
                      改角色
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="pager">
            <span class="muted small">共 {{ store.users.total }} 条</span>
            <div class="pager-btns">
              <button
                type="button"
                :disabled="!store.hasPrev(store.users) || store.users.loading"
                data-testid="admin-users-prev"
                @click="store.loadUsers(Math.max(0, store.users.offset - ADMIN_PAGE_SIZE))"
              >
                上一页
              </button>
              <button
                type="button"
                :disabled="!store.hasNext(store.users) || store.users.loading"
                data-testid="admin-users-next"
                @click="store.loadUsers(store.users.offset + ADMIN_PAGE_SIZE)"
              >
                下一页
              </button>
            </div>
          </div>
        </section>

        <!-- ============ 房间 ============ -->
        <section class="card" v-else-if="store.section === 'rooms'" data-testid="admin-rooms">
          <header class="sec-head">
            <h2><BrandIcon name="video" decorative :size="15" />房间<span class="muted small">（含未公开）</span></h2>
            <form class="search" @submit.prevent="store.loadRooms(0)">
              <label class="sr-only" for="admin-room-search">按房间码或标题搜索</label>
              <input
                id="admin-room-search"
                v-model="store.roomSearch"
                placeholder="搜索房间码或标题"
                data-testid="admin-room-search"
              />
              <label class="sr-only" for="admin-room-public">公开性筛选</label>
              <select id="admin-room-public" v-model="store.roomPublic" data-testid="admin-room-public">
                <option value="">全部</option>
                <option value="true">仅公开</option>
                <option value="false">仅未公开</option>
              </select>
              <button type="submit" :disabled="store.rooms.loading" :aria-busy="store.rooms.loading">
                <BrandIcon name="tips" decorative :size="14" />
                搜索
              </button>
            </form>
          </header>

          <div class="error-bar" v-if="store.rooms.error">
            <BrandIcon name="alert-triangle" label="加载失败" :size="15" />
            <span class="msg">{{ store.rooms.error }}</span>
            <button type="button" class="icon-btn" aria-label="重试" title="重试" @click="store.loadRooms(store.rooms.offset)">
              <BrandIcon name="refresh" decorative :size="15" />
            </button>
          </div>

          <p class="muted" v-if="store.rooms.loading && store.rooms.items.length === 0">加载中…</p>
          <p class="muted" v-else-if="store.rooms.items.length === 0">没有匹配的房间。</p>

          <div class="table-wrap" v-else>
            <table class="mono-table">
              <thead>
                <tr>
                  <th>房间码</th>
                  <th>标题</th>
                  <th>房主 id</th>
                  <th>在座</th>
                  <th>状态</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="room in store.rooms.items" :key="room.roomId" :data-testid="`admin-room-row-${room.roomId}`">
                  <td class="mono">{{ room.roomId }}</td>
                  <td class="title-cell">{{ room.title || '未命名房间' }}</td>
                  <td class="muted">{{ room.ownerUserId > 0 ? room.ownerUserId : '无' }}</td>
                  <td>{{ room.memberCount }}</td>
                  <td class="flags">
                    <span class="badge" :class="{ ok: room.isPublic }">{{ room.isPublic ? '公开' : '未公开' }}</span>
                    <span class="badge" v-if="room.hostOnline">主播在线</span>
                    <span class="badge" v-else-if="room.hostOffline">主播离线中</span>
                    <span class="badge" v-if="room.hasPassword"><BrandIcon name="lock" decorative :size="11" /></span>
                    <span class="badge danger" v-if="room.metadataMissing" title="库中缺该房间的元数据行">元数据缺失</span>
                  </td>
                  <td class="ops">
                    <button
                      type="button"
                      class="danger"
                      :disabled="roomBusy(room)"
                      :data-testid="`admin-room-close-${room.roomId}`"
                      @click="askClose(room)"
                    >
                      <BrandIcon name="alert-triangle" decorative :size="13" />
                      强制关闭
                    </button>
                    <button
                      type="button"
                      :disabled="roomBusy(room) || !room.isPublic"
                      :data-testid="`admin-room-unpublish-${room.roomId}`"
                      @click="askUnpublish(room)"
                    >
                      <BrandIcon name="close" decorative :size="13" />
                      下架
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="pager">
            <span class="muted small">共 {{ store.rooms.total }} 条</span>
            <div class="pager-btns">
              <button
                type="button"
                :disabled="!store.hasPrev(store.rooms) || store.rooms.loading"
                data-testid="admin-rooms-prev"
                @click="store.loadRooms(Math.max(0, store.rooms.offset - ADMIN_PAGE_SIZE))"
              >
                上一页
              </button>
              <button
                type="button"
                :disabled="!store.hasNext(store.rooms) || store.rooms.loading"
                data-testid="admin-rooms-next"
                @click="store.loadRooms(store.rooms.offset + ADMIN_PAGE_SIZE)"
              >
                下一页
              </button>
            </div>
          </div>
        </section>

        <!-- ============ 指标 ============ -->
        <section class="card" v-else-if="store.section === 'metrics'" data-testid="admin-metrics">
          <header class="sec-head">
            <h2><BrandIcon name="temperature" decorative :size="15" />运行指标</h2>
            <button
              type="button"
              class="icon-btn"
              :disabled="store.metrics.loading"
              :aria-busy="store.metrics.loading"
              aria-label="刷新指标"
              title="刷新指标"
              data-testid="admin-metrics-refresh"
              @click="store.loadMetrics()"
            >
              <BrandIcon name="refresh" decorative :size="16" :class="{ spinning: store.metrics.loading }" />
            </button>
          </header>

          <div class="error-bar" v-if="store.metrics.error">
            <BrandIcon name="alert-triangle" label="加载失败" :size="15" />
            <span class="msg">{{ store.metrics.error }}</span>
          </div>

          <!-- 键值卡片式：服务端某个来源缺失时**只少一张卡**，不是一个报错整页。 -->
          <div class="kv-grid">
            <div class="kv" data-testid="admin-metric-connections">
              <span class="k">连接</span>
              <span class="v">{{ store.metrics.data.connections ?? '-' }}</span>
            </div>
            <div class="kv" data-testid="admin-metric-online-users">
              <span class="k">在线用户</span>
              <span class="v">{{ store.metrics.data.onlineUsers ?? '-' }}</span>
            </div>
            <div class="kv" data-testid="admin-metric-rooms">
              <span class="k">房间</span>
              <span class="v">{{ store.metrics.data.rooms ?? '-' }}</span>
            </div>
            <div class="kv" data-testid="admin-metric-write-queue">
              <span class="k">写队列（积压 / 入队 / 成功 / 失败 / 丢弃）</span>
              <span class="v">
                {{ store.metrics.data.writeQueue?.pending ?? '-' }} /
                {{ store.metrics.data.writeQueue?.queued ?? '-' }} /
                {{ store.metrics.data.writeQueue?.succeeded ?? '-' }} /
                {{ store.metrics.data.writeQueue?.failed ?? '-' }} /
                {{ store.metrics.data.writeQueue?.dropped ?? '-' }}
              </span>
              <span class="muted small">最近一次写入耗时 {{ store.metrics.data.writeQueue?.lastLatencyMs ?? '-' }} ms</span>
            </div>
            <div class="kv" data-testid="admin-metric-logs">
              <span class="k">日志缓冲（保留 / 累计 / 丢弃）</span>
              <span class="v">
                {{ store.metrics.data.logs?.kept ?? '-' }} / {{ store.metrics.data.logs?.total ?? '-' }} /
                {{ store.metrics.data.logs?.dropped ?? '-' }}
              </span>
            </div>
          </div>
        </section>

        <!-- ============ 日志 ============ -->
        <section class="card" v-else-if="store.section === 'logs'" data-testid="admin-logs">
          <header class="sec-head">
            <h2><BrandIcon name="chat" decorative :size="15" />服务端日志<span class="muted small">（新 → 旧）</span></h2>
            <button
              type="button"
              class="icon-btn"
              :disabled="store.logs.loading"
              :aria-busy="store.logs.loading"
              aria-label="刷新日志"
              title="刷新日志"
              data-testid="admin-logs-refresh"
              @click="store.loadLogs(store.logs.offset)"
            >
              <BrandIcon name="refresh" decorative :size="16" :class="{ spinning: store.logs.loading }" />
            </button>
          </header>

          <div class="error-bar" v-if="store.logs.error">
            <BrandIcon name="alert-triangle" label="加载失败" :size="15" />
            <span class="msg">{{ store.logs.error }}</span>
          </div>

          <p class="muted" v-if="store.logs.loading && store.logs.items.length === 0">加载中…</p>
          <p class="muted" v-else-if="store.logs.items.length === 0">日志缓冲是空的（服务端还没写任何行，或缓冲未启用）。</p>

          <!-- 日志一律**文本插值**：日志行里含用户可控文本（昵称、路径、房间码），
               任何拼 HTML 的写法都是注入面。等宽字体是为了对齐时间戳与级别。 -->
          <ol class="log-lines mono" v-else data-testid="admin-log-lines">
            <li v-for="(line, index) in store.logs.items" :key="`${store.logs.offset}-${index}`">{{ line }}</li>
          </ol>

          <div class="pager">
            <span class="muted small">共 {{ store.logs.total }} 行</span>
            <div class="pager-btns">
              <button
                type="button"
                :disabled="!store.hasPrev(store.logs) || store.logs.loading"
                data-testid="admin-logs-prev"
                @click="store.loadLogs(Math.max(0, store.logs.offset - ADMIN_PAGE_SIZE))"
              >
                上一页
              </button>
              <button
                type="button"
                :disabled="!store.hasNext(store.logs) || store.logs.loading"
                data-testid="admin-logs-next"
                @click="store.loadLogs(store.logs.offset + ADMIN_PAGE_SIZE)"
              >
                下一页
              </button>
            </div>
          </div>
        </section>

        <!-- ============ 审计 ============ -->
        <section class="card" v-else data-testid="admin-audit">
          <header class="sec-head">
            <h2><BrandIcon name="shield-done" decorative :size="15" />审计<span class="muted small">（新 → 旧）</span></h2>
            <button
              type="button"
              class="icon-btn"
              :disabled="store.audit.loading"
              :aria-busy="store.audit.loading"
              aria-label="刷新审计"
              title="刷新审计"
              data-testid="admin-audit-refresh"
              @click="store.loadAudit(store.audit.offset)"
            >
              <BrandIcon name="refresh" decorative :size="16" :class="{ spinning: store.audit.loading }" />
            </button>
          </header>

          <div class="error-bar" v-if="store.audit.error">
            <BrandIcon name="alert-triangle" label="加载失败" :size="15" />
            <span class="msg">{{ store.audit.error }}</span>
          </div>

          <p class="muted" v-if="store.audit.loading && store.audit.items.length === 0">加载中…</p>
          <p class="muted" v-else-if="store.audit.items.length === 0">没有审计记录。</p>

          <div class="table-wrap" v-else>
            <table class="mono-table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>操作者</th>
                  <th>动作</th>
                  <th>目标</th>
                  <th>IP</th>
                  <th>明细</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in store.audit.items" :key="row.id" :data-testid="`admin-audit-row-${row.id}`">
                  <td class="muted">{{ when(row.createdAt) }}</td>
                  <td class="muted">{{ row.actorId }}</td>
                  <td>{{ row.action }}</td>
                  <td class="mono">{{ row.targetType }}:{{ row.targetId }}</td>
                  <td class="muted mono">{{ row.ip || '-' }}</td>
                  <!-- detail 是 jsonb 的**原文文本**（服务端刻意不解析成结构）：照文本渲染。 -->
                  <td class="detail-cell mono">{{ row.detail }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="pager">
            <!-- 审计响应**没有** total（服务端不做 COUNT）：用"本页是否满页"判断还有没有更多，
                 并如实说明这是"是否满页"而不是精确总数。 -->
            <span class="muted small">
              本页 {{ store.audit.items.length }} 行{{ store.hasNext(store.audit) ? '（可能还有更多）' : '' }}
            </span>
            <div class="pager-btns">
              <button
                type="button"
                :disabled="!store.hasPrev(store.audit) || store.audit.loading"
                data-testid="admin-audit-prev"
                @click="store.loadAudit(Math.max(0, store.audit.offset - ADMIN_PAGE_SIZE))"
              >
                上一页
              </button>
              <button
                type="button"
                :disabled="!store.hasNext(store.audit) || store.audit.loading"
                data-testid="admin-audit-next"
                @click="store.loadAudit(store.audit.offset + ADMIN_PAGE_SIZE)"
              >
                下一页
              </button>
            </div>
          </div>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.admin {
  max-width: 1100px;
  margin: 0 auto;
  padding: 24px 20px 44px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.admin-head h1 {
  margin: 0 0 4px;
  font-size: 22px;
  display: flex;
  align-items: center;
  gap: 7px;
}

.admin-head p {
  margin: 0;
  font-size: 12px;
}

.tabs {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  border-bottom: 1px solid var(--border);
  padding-bottom: 2px;
}

button.tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: transparent;
  border: 1px solid transparent;
  border-bottom: 2px solid transparent;
  border-radius: 6px 6px 0 0;
  padding: 6px 12px;
  color: var(--text-dim);
  font-size: 13px;
}

button.tab.active {
  color: var(--text);
  border-bottom-color: var(--accent);
}

.denied h1 {
  margin: 0 0 10px;
  font-size: 20px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.denied p {
  margin: 0 0 8px;
  font-size: 13px;
  line-height: 1.7;
}

.actions {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 12px;
}

.actions a.back {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
}

.confirm {
  border-color: var(--danger);
}

.confirm h2 {
  margin: 0 0 8px;
  font-size: 15px;
  display: flex;
  align-items: center;
  gap: 7px;
}

.confirm p {
  margin: 0 0 10px;
  line-height: 1.7;
}

.confirm label {
  margin-top: 8px;
}

.confirm select {
  font: inherit;
  color: var(--text);
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px;
}

button.danger {
  border-color: var(--danger);
  color: #ffdada;
}

.sec-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}

.sec-head h2 {
  margin: 0;
  font-size: 15px;
  display: flex;
  align-items: center;
  gap: 7px;
  flex-wrap: wrap;
}

.search {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

.search input {
  width: 200px;
}

.search select {
  font: inherit;
  color: var(--text);
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px;
}

.search button {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

/* 宽表在窄屏上横向滚动，而不是把操作按钮挤出屏幕。 */
.table-wrap {
  overflow-x: auto;
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

th,
td {
  text-align: left;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}

th {
  color: var(--text-dim);
  font-weight: 400;
  font-size: 12px;
}

.title-cell {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.detail-cell {
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.flags,
.ops {
  display: flex;
  gap: 6px;
  align-items: center;
  flex-wrap: wrap;
}

.ops button {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 5px 10px;
  font-size: 12px;
}

.kv-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: 10px;
}

.kv {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px;
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 8px;
}

.kv .k {
  color: var(--text-dim);
  font-size: 12px;
}

.kv .v {
  font-size: 18px;
}

.log-lines {
  margin: 0;
  padding: 0 0 0 2.2em;
  max-height: 60vh;
  overflow: auto;
  font-size: 12px;
  line-height: 1.6;
}

/* 日志行必须能横向滚动而不是折成一片乱麻：它们是"一行一条"的文本。 */
.log-lines li {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 12px;
}

.pager-btns {
  display: flex;
  gap: 8px;
}

.small {
  font-size: 12px;
}

.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 5px;
  background: transparent;
  border-color: transparent;
  color: var(--text-dim);
  line-height: 0;
}

.icon-btn:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--border);
}

.msg {
  flex: 1;
  min-width: 0;
}

/* 只给读屏的标签（搜索框在视觉上由 placeholder 说明）。 */
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

.spinning {
  animation: admin-spin 0.9s linear infinite;
}

@keyframes admin-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 460px) {
  .admin {
    padding: 16px 12px 32px;
  }

  .admin-head h1 {
    font-size: 19px;
  }

  .search input {
    width: 100%;
  }

  .kv-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
