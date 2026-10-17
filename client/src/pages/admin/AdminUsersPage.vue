<script setup lang="ts">
/**
 * 管理端 · 用户管理视图（`/admin` 的第一区块，wayfinder #23）。
 *
 * 一条纪律贯穿全页（铁律 I3）：**授权 100% 在服务端**。本页不发任何"我是不是 admin"
 * 的判断，也不因为本地缓存 `profile.role !== 'admin'` 就不发请求——那只是入口是否显示
 * 的体验优化（见 AppHeader）。真正的门是服务端 `RequireAdmin`：照常发请求，拿 403 就
 * 渲染「无权限」，拿 401 就渲染「会话失效」。两者文案与出口不同，因为它们是两件事。
 *
 * 四个动作都是**写操作**（封禁 / 解封 / 改角色 / 绑邮箱），全部要二次确认：
 *   - 二次确认用页内的确认条而不是 `window.confirm`：`confirm` 在 headless 验收脚本里
 *     是阻塞弹窗，且无法说明"这次具体改了什么"；
 *   - 确认条把**动作 + 对象 + 新值**原样写出来（"把 xxx 的角色从 user 改成 admin"），
 *     而不是一句"确定吗"——后者是让人盲点。
 *
 * 所有服务端文本（用户名、昵称、邮箱、错误文案）一律走 `{{ }}` 文本插值，
 * 绝不拼 HTML：这些字段用户可控。
 */
import { computed, onMounted, ref } from 'vue'
import BrandIcon from '../../components/ui/BrandIcon.vue'
import { ADMIN_PAGE_SIZE, useAdminStore } from '../../stores/admin'
import type { AdminUser } from '../../api/admin'

const store = useAdminStore()

/** 搜索框的本地草稿：按回车/点按钮才提交，避免每敲一个字就打一次服务端。 */
const searchDraft = ref('')

/** 正在确认的危险动作（null = 没有确认条）。 */
const pending = ref<null | {
  kind: 'ban' | 'unban' | 'role' | 'email'
  user: AdminUser
  /** 目标值：role 用 'admin'/'user'；email 用新邮箱；封禁/解封不用。 */
  value: string
  /** 封禁原因（只对 ban 有意义；服务端只把它写进审计明细）。 */
  reason: string
}>(null)

/** 确认条里的输入（邮箱 / 封禁原因）。 */
const emailDraft = ref('')
const reasonDraft = ref('')

/** 危险动作的校验错误（本地校验；服务端的 400 走 store.noticeError）。 */
const confirmError = ref('')

const users = computed(() => store.users.items)
const total = computed(() => store.users.total)

onMounted(() => {
  // 直接进本区块（不经 bootstrap 的 users 探针之外的分支）：本页就是用户视图。
  if (store.section !== 'users') store.section = 'users'
  void store.bootstrap()
})

function submitSearch() {
  store.userSearch = searchDraft.value.trim()
  void store.loadUsers(0)
}

function clearSearch() {
  searchDraft.value = ''
  store.userSearch = ''
  void store.loadUsers(0)
}

/** 翻页：offset 由"当前偏移 ± 每页条数"算出，不缓存页码（total 会变）。 */
function prevPage() {
  void store.loadUsers(Math.max(0, store.users.offset - ADMIN_PAGE_SIZE))
}

function nextPage() {
  void store.loadUsers(store.users.offset + ADMIN_PAGE_SIZE)
}

// ---------- 危险动作：先开确认条，再执行 ----------

function askBan(user: AdminUser) {
  confirmError.value = ''
  reasonDraft.value = ''
  pending.value = { kind: 'ban', user, value: '', reason: '' }
}

function askUnban(user: AdminUser) {
  confirmError.value = ''
  pending.value = { kind: 'unban', user, value: '', reason: '' }
}

function askRole(user: AdminUser) {
  confirmError.value = ''
  pending.value = { kind: 'role', user, value: user.role === 'admin' ? 'user' : 'admin', reason: '' }
}

function askEmail(user: AdminUser) {
  confirmError.value = ''
  emailDraft.value = user.email
  pending.value = { kind: 'email', user, value: '', reason: '' }
}

function cancelPending() {
  pending.value = null
  confirmError.value = ''
}

/**
 * 执行确认条里的动作。
 *
 * 邮箱走**服务端校验**（格式不合法它返回 400，文案原样显示），所以这里只挡"空串"
 * 这种明显误操作 —— 前端重写一遍邮箱正则只会在两边规则不一致时给出错误结论。
 */
async function confirmPending() {
  const p = pending.value
  if (!p) return
  confirmError.value = ''

  if (p.kind === 'ban') {
    const ok = await store.setUserStatus(p.user, 'banned', reasonDraft.value.trim())
    if (ok) pending.value = null
    return
  }
  if (p.kind === 'unban') {
    const ok = await store.setUserStatus(p.user, 'active', '')
    if (ok) pending.value = null
    return
  }
  if (p.kind === 'role') {
    const ok = await store.setUserRole(p.user, p.value, '')
    if (ok) pending.value = null
    return
  }
  // email
  const email = emailDraft.value.trim()
  if (email === '') {
    // 空串在服务端是"解绑"，语义与"我手滑清空了输入框"完全不同：这里要求显式确认。
    confirmError.value = '邮箱不能为空。要解绑请把输入框留空后点「确认解绑」。'
    return
  }
  const ok = await store.setUserEmail(p.user, email)
  if (ok) pending.value = null
}

/** 解绑邮箱：服务端约定 `email: ''` = 解绑（同时清 email_verified_at）。 */
async function confirmUnbindEmail() {
  const p = pending.value
  if (!p || p.kind !== 'email') return
  confirmError.value = ''
  const ok = await store.setUserEmail(p.user, '')
  if (ok) pending.value = null
}

/** 时间显示：服务端给的是 RFC3339，这里只做"日期 + 时分"的截断，不重新解析时区。 */
function shortTime(iso: string): string {
  if (!iso) return '—'
  return iso.replace('T', ' ').slice(0, 16)
}

function roleText(role: string): string {
  return role === 'admin' ? '管理员' : '普通用户'
}

function statusText(status: string): string {
  return status === 'banned' ? '已封禁' : '正常'
}
</script>

<template>
  <div class="page">
    <main class="body">
      <header class="head">
        <h1 class="title">管理端 · 用户</h1>
        <nav class="switcher" aria-label="管理端区块">
          <RouterLink class="tab on" :to="{ name: 'admin' }">用户</RouterLink>
          <RouterLink class="tab" :to="{ name: 'admin-rooms' }">房间</RouterLink>
          <RouterLink class="tab" :to="{ name: 'admin-ops' }">运维</RouterLink>
        </nav>
      </header>

      <!-- 整页结论：服务端说了算（I3）。401 与 403 分开给出口：
           前者是会话没了（给登录），后者是会话好好的只是没权限（给首页）。
           把 403 的人送去登录页，等于告诉他"你的登录出了问题"，而那是假的。 -->
      <section v-if="store.isDenied" class="denied" role="alert">
        <h2 class="denied-head">
          <BrandIcon name="alert-triangle" decorative :size="16" />
          <template v-if="store.denied === 'forbidden'">无权限访问管理端</template>
          <template v-else-if="store.denied === 'unauthenticated'">登录状态已失效</template>
          <template v-else>管理端暂时不可用</template>
        </h2>
        <p class="denied-text">{{ store.deniedText || '服务端未返回说明。' }}</p>
        <div class="denied-acts">
          <RouterLink v-if="store.denied === 'unauthenticated'" class="primary" :to="{ name: 'login', query: { redirect: '/admin' } }">
            重新登录
          </RouterLink>
          <RouterLink v-else class="primary" :to="{ name: 'home' }">返回首页</RouterLink>
          <button type="button" class="ghost" @click="store.bootstrap()">重试</button>
        </div>
      </section>

      <template v-else>
        <!-- 搜索 + 分页信息 -->
        <form class="bar" role="search" @submit.prevent="submitSearch">
          <label class="sr" for="admin-user-search">搜索用户</label>
          <input
            id="admin-user-search"
            v-model="searchDraft"
            type="search"
            placeholder="按用户名 / 昵称 / 邮箱搜索"
            data-testid="admin-user-search"
          />
          <button type="submit" class="primary" :disabled="store.users.loading">搜索</button>
          <button v-if="store.userSearch" type="button" class="ghost" @click="clearSearch">清除</button>
        </form>

        <!-- 操作反馈：成功与失败分开呈现（成功刷新了列表，失败没刷新） -->
        <p v-if="store.notice" class="ok" role="status" data-testid="admin-notice">{{ store.notice }}</p>
        <p v-if="store.noticeError" class="alert" role="alert" data-testid="admin-notice-error">{{ store.noticeError }}</p>

        <!-- 确认条：把"动作 + 对象 + 新值"原样写出来，而不是一句"确定吗" -->
        <section v-if="pending" class="confirm" role="alertdialog" aria-labelledby="confirm-head" data-testid="admin-confirm">
          <h2 id="confirm-head" class="confirm-head">
            <BrandIcon name="alert-triangle" decorative :size="15" />
            <template v-if="pending.kind === 'ban'">确认封禁</template>
            <template v-else-if="pending.kind === 'unban'">确认解封</template>
            <template v-else-if="pending.kind === 'role'">确认调整角色</template>
            <template v-else>确认修改邮箱</template>
          </h2>
          <p class="confirm-text">
            <template v-if="pending.kind === 'ban'">
              将封禁 <strong>{{ pending.user.username }}</strong>。已签发的登录凭证立即失效，在跑的长连接会被断开。
            </template>
            <template v-else-if="pending.kind === 'unban'">
              将解封 <strong>{{ pending.user.username }}</strong>，该账号可重新登录。
            </template>
            <template v-else-if="pending.kind === 'role'">
              将 <strong>{{ pending.user.username }}</strong> 的角色从「{{ roleText(pending.user.role) }}」改为「{{ roleText(pending.value) }}」。
            </template>
            <template v-else>
              将 <strong>{{ pending.user.username }}</strong> 的邮箱改为下面的值，并同时标记为已验证（管理员代改视为已验证）。
            </template>
          </p>

          <template v-if="pending.kind === 'ban'">
            <label for="ban-reason">封禁原因（可选，只进审计明细）</label>
            <input id="ban-reason" v-model="reasonDraft" type="text" placeholder="例如：发布违规内容" />
          </template>

          <template v-if="pending.kind === 'email'">
            <label for="bind-email">新邮箱</label>
            <input id="bind-email" v-model="emailDraft" type="email" placeholder="name@example.com" data-testid="admin-email-input" />
          </template>

          <p v-if="confirmError" class="alert" role="alert">{{ confirmError }}</p>

          <div class="confirm-acts">
            <button type="button" class="primary" :disabled="store.busyKey !== ''" data-testid="admin-confirm-yes" @click="confirmPending">
              确认
            </button>
            <button
              v-if="pending.kind === 'email'"
              type="button"
              class="ghost"
              :disabled="store.busyKey !== ''"
              @click="confirmUnbindEmail"
            >
              确认解绑
            </button>
            <button type="button" class="ghost" :disabled="store.busyKey !== ''" @click="cancelPending">取消</button>
          </div>
        </section>

        <p v-if="store.booting || store.users.loading" class="dim">加载中…</p>
        <p v-else-if="store.users.error" class="alert" role="alert">{{ store.users.error }}</p>
        <p v-else-if="users.length === 0" class="dim">
          {{ store.userSearch ? '没有匹配的用户。' : '还没有用户。' }}
        </p>

        <template v-else>
          <!-- 桌面：表格。375px 下横向滚动会丢掉操作列，所以窄屏换卡片（见下方 .cards）。 -->
          <div class="table-wrap">
            <table class="table">
              <thead>
                <tr>
                  <th scope="col">用户</th>
                  <th scope="col">邮箱</th>
                  <th scope="col">角色</th>
                  <th scope="col">状态</th>
                  <th scope="col">最近活跃</th>
                  <th scope="col">操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="u in users" :key="u.id">
                  <td>
                    <span class="uname">{{ u.displayName || u.username }}</span>
                    <span class="mono dim">@{{ u.username }} · #{{ u.id }}</span>
                  </td>
                  <td>
                    <span class="mono">{{ u.email || '—' }}</span>
                    <!-- 三态：true 已验证 / false 未验证 / undefined 服务端未返回。
                         把"没返回"谎报成"未验证"会让人去点一次没必要的重发。 -->
                    <span v-if="u.emailVerified === true" class="badge ok">已验证</span>
                    <span v-else-if="u.emailVerified === false" class="badge">未验证</span>
                    <span v-else class="badge dim">状态未知</span>
                  </td>
                  <td>
                    <span class="badge" :class="{ admin: u.role === 'admin' }">{{ roleText(u.role) }}</span>
                  </td>
                  <td>
                    <span class="badge" :class="{ banned: u.status === 'banned' }">{{ statusText(u.status) }}</span>
                  </td>
                  <td class="mono dim">{{ shortTime(u.lastSeenAt) }}</td>
                  <td class="acts">
                    <button type="button" :disabled="store.busyKey !== ''" @click="askRole(u)">
                      {{ u.role === 'admin' ? '降为普通' : '设为管理员' }}
                    </button>
                    <button v-if="u.status === 'banned'" type="button" :disabled="store.busyKey !== ''" @click="askUnban(u)">
                      解封
                    </button>
                    <button v-else type="button" class="danger" :disabled="store.busyKey !== ''" @click="askBan(u)">
                      封禁
                    </button>
                    <button type="button" :disabled="store.busyKey !== ''" @click="askEmail(u)">改邮箱</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 窄屏卡片：同一份数据，操作按钮不换行、不横向滚动 -->
          <ul class="cards">
            <li v-for="u in users" :key="u.id" class="card">
              <div class="card-head">
                <span class="uname">{{ u.displayName || u.username }}</span>
                <span class="badge" :class="{ banned: u.status === 'banned' }">{{ statusText(u.status) }}</span>
              </div>
              <p class="mono dim card-line">@{{ u.username }} · #{{ u.id }}</p>
              <p class="mono card-line">{{ u.email || '—' }}</p>
              <p class="card-line">
                <span class="badge" :class="{ admin: u.role === 'admin' }">{{ roleText(u.role) }}</span>
                <span v-if="u.emailVerified === true" class="badge ok">已验证</span>
                <span v-else-if="u.emailVerified === false" class="badge">未验证</span>
                <span v-else class="badge dim">状态未知</span>
              </p>
              <p class="mono dim card-line">最近活跃 {{ shortTime(u.lastSeenAt) }}</p>
              <div class="card-acts">
                <button type="button" :disabled="store.busyKey !== ''" @click="askRole(u)">
                  {{ u.role === 'admin' ? '降为普通' : '设为管理员' }}
                </button>
                <button v-if="u.status === 'banned'" type="button" :disabled="store.busyKey !== ''" @click="askUnban(u)">解封</button>
                <button v-else type="button" class="danger" :disabled="store.busyKey !== ''" @click="askBan(u)">封禁</button>
                <button type="button" :disabled="store.busyKey !== ''" @click="askEmail(u)">改邮箱</button>
              </div>
            </li>
          </ul>

          <nav class="pager" aria-label="分页">
            <button type="button" :disabled="!store.hasPrev(store.users) || store.users.loading" @click="prevPage">上一页</button>
            <span class="dim mono">
              {{ store.users.offset + 1 }}–{{ store.users.offset + users.length }} / {{ total }}
            </span>
            <button type="button" :disabled="!store.hasNext(store.users) || store.users.loading" @click="nextPage">下一页</button>
          </nav>
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
  width: min(1120px, 100%);
  margin: 0 auto;
  padding: 32px 20px 64px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.title {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: clamp(28px, 5vw, 42px);
}
/* 三张管理端视图共用同一套分区导航（用户 / 房间 / 运维） */
.switcher {
  display: flex;
  gap: 2px;
}
.tab {
  padding: 6px 16px;
  color: var(--dim);
  border-bottom: 2px solid transparent;
  font-size: 13px;
}
.tab:hover {
  color: var(--text);
  text-decoration: none;
}
.tab.on {
  color: var(--text);
  border-bottom-color: var(--accent);
}

/* 屏幕阅读器专用标签：视觉上不占位，读屏仍可读 */
.sr {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}

.bar {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.bar input {
  flex: 1;
  min-width: 200px;
}

button.primary {
  padding: 8px 18px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
}
button.ghost {
  padding: 8px 16px;
  border: 1px solid var(--line);
  color: var(--dim);
}
button.ghost:hover {
  color: var(--text);
  border-color: var(--accent);
}
button.danger {
  color: var(--danger);
  border-color: rgba(255, 92, 92, 0.4);
}
button.danger:hover {
  color: #ffdada;
  border-color: var(--danger);
}

.ok {
  margin: 0;
  padding: 8px 10px;
  border: 1px solid var(--ok);
  background: rgba(61, 220, 151, 0.1);
  font-size: 12px;
  color: #d6ffe9;
}
.alert {
  margin: 0;
  padding: 8px 10px;
  border: 1px solid var(--danger);
  background: rgba(255, 92, 92, 0.12);
  font-size: 12px;
  color: #ffdada;
}

.denied {
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.denied-head {
  margin: 0;
  font-size: 15px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.denied-text {
  margin: 0;
  font-size: 13px;
  color: var(--dim);
}
.denied-acts {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.denied-acts .primary {
  display: inline-flex;
  align-items: center;
  padding: 8px 18px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
}
.denied-acts .primary:hover {
  text-decoration: none;
}

.confirm {
  border: 1px solid var(--line-strong);
  background: rgba(180, 108, 255, 0.08);
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.confirm-head {
  margin: 0;
  font-size: 14px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.confirm-text {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
}
.confirm-acts {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}

.table-wrap {
  border: 1px solid var(--line);
  overflow-x: auto;
}
.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.table th,
.table td {
  padding: 10px 12px;
  text-align: left;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
}
.table th {
  font-size: 12px;
  color: var(--dim);
  font-weight: 400;
  white-space: nowrap;
}
.table tbody tr:last-child td {
  border-bottom: 0;
}
.uname {
  display: block;
  color: var(--text);
}
.mono {
  font-family: var(--font-mono);
  font-size: 11px;
}
.acts {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.acts button,
.card-acts button {
  padding: 5px 12px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 12px;
}
.acts button:hover,
.card-acts button:hover {
  color: var(--text);
  border-color: var(--accent);
}
.acts button:disabled,
.card-acts button:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.badge {
  display: inline-block;
  margin-left: 4px;
  padding: 1px 6px;
  border: 1px solid var(--line);
  font-size: 11px;
  color: var(--dim);
  white-space: nowrap;
}
.badge.admin {
  color: var(--accent);
  border-color: var(--line-strong);
}
.badge.banned {
  color: var(--danger);
  border-color: rgba(255, 92, 92, 0.4);
}
.badge.ok {
  color: var(--ok);
  border-color: rgba(61, 220, 151, 0.4);
}

/* 窄屏卡片：默认隐藏，≤760px 显示；表格同时隐藏。
   两档不是"缩放同一份布局"——表格在 375px 上必然横向滚动，操作列会被推出视野。 */
.cards {
  display: none;
  margin: 0;
  padding: 0;
  list-style: none;
  flex-direction: column;
  gap: 10px;
}
.card {
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 5px;
}
.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.card-line {
  margin: 0;
}
.card-acts {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-top: 4px;
}

.pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
  padding-top: 4px;
}
.pager button {
  padding: 7px 16px;
  border: 1px solid var(--line);
  color: var(--dim);
}
.pager button:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--accent);
}
.pager button:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

@media (max-width: 760px) {
  .table-wrap {
    display: none;
  }
  .cards {
    display: flex;
  }
}
</style>
