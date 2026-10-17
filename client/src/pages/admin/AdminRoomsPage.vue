<script setup lang="ts">
/**
 * 管理端 · 房间管理视图（wayfinder #24）。
 *
 * 与用户视图（#23）同构：授权 100% 在服务端（I3），本页只渲染服务端裁决。
 * 三处不同，都是这张票自己要求或后端契约决定的：
 *
 *   1. **分页/过滤进 URL**（验收第 3 条）：`?search=&public=&offset=` 可分享、刷新不丢。
 *      用 `router.replace` 而不是 `push`——翻页不该在浏览器历史里堆一串条目，
 *      否则"返回"要按十几次才回到上一页。
 *   2. **列表含未公开房间**：后端把"内存快照"与"库元数据"做并集，所以同一份列表里
 *      既有正在跑的房间，也有"刚才被关掉的历史行"。`inMemory` 区分这两者，
 *      而 `metadataMissing` 表示"这个房间在库里没有元数据行"——它无法满足任何公开性筛选，
 *      界面上要标出来，否则"筛了公开房却没看到它"看起来像 bug。
 *   3. **两个动作的后果不同**，确认文案必须说清：
 *      强关 = 断开房内所有连接（不可撤销，房间码立刻失效）；
 *      下架 = 只是从公开列表消失（房间还在跑，房主仍可用房间码邀请）。
 *
 * 幂等提醒：下架是幂等的（后端对已未公开的房间仍返回 200，且不写审计行）。
 * 所以界面**不能**把"下架成功"当成"这次真的改变了什么"——文案只说"现在未公开"。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from '../../components/ui/BrandIcon.vue'
import { ADMIN_PAGE_SIZE, useAdminStore } from '../../stores/admin'
import type { AdminRoom } from '../../api/admin'

const store = useAdminStore()
const route = useRoute()
const router = useRouter()

/** 搜索框草稿：回车/点按钮才提交，避免逐字打服务端。 */
const searchDraft = ref('')

/** 正在确认的危险动作。 */
const pending = ref<null | { kind: 'close' | 'unpublish'; room: AdminRoom }>(null)

const rooms = computed(() => store.rooms.items)
const total = computed(() => store.rooms.total)

/**
 * 把 URL query 读进 store（刷新 / 分享链接 / 前进后退都走这里）。
 *
 * `public` 只接受 '' / 'true' / 'false'：服务端对非法值返回 400，前端不自己猜
 * （猜错会让"筛了非法值"看起来像"筛了个空结果"）。
 */
function readQuery(): { search: string; public: string; offset: number } {
  const q = route.query
  const pick = (v: unknown): string => (typeof v === 'string' ? v : '')
  const pubRaw = pick(q.public)
  const pub = pubRaw === 'true' || pubRaw === 'false' ? pubRaw : ''
  const offsetRaw = Number.parseInt(pick(q.offset), 10)
  const offset = Number.isFinite(offsetRaw) && offsetRaw > 0 ? offsetRaw : 0
  return { search: pick(q.search), public: pub, offset }
}

/** 把当前筛选写回 URL（`replace`：翻页不堆历史条目）。 */
function writeQuery(): void {
  const query: Record<string, string> = {}
  if (store.userSearch !== '') query.search = store.userSearch
  if (store.roomPublic !== '') query.public = store.roomPublic
  if (store.rooms.offset > 0) query.offset = String(store.rooms.offset)
  void router.replace({ name: 'admin-rooms', query })
}

/** 按 URL 重新拉一页。 */
async function loadFromQuery(): Promise<void> {
  const { search, public: pub, offset } = readQuery()
  store.userSearch = search
  store.roomSearch = search
  store.roomPublic = pub
  searchDraft.value = search
  await store.loadRooms(offset)
}

onMounted(() => {
  store.section = 'rooms'
  // 首次进入：把 URL 里的筛选读进来再拉数据（这样刷新/分享链接能复现同一屏）。
  void loadFromQuery()
})

/**
 * 地址栏变化（浏览器前进/后退、外部改 query）→ 重新拉。
 *
 * 只监听 `route.query` 的字符串化结果：写成监听整个 `route` 会在**我们自己**
 * 调 `replace` 之后再次触发，形成"拉数据 → 改 URL → 又拉数据"的循环。
 */
watch(
  () => JSON.stringify(route.query),
  (next, prev) => {
    if (next === prev) return
    if (route.name !== 'admin-rooms') return
    void loadFromQuery()
  },
)

function submitSearch() {
  store.roomSearch = searchDraft.value.trim()
  store.userSearch = store.roomSearch
  void store.loadRooms(0).then(writeQuery)
}

function clearFilters() {
  searchDraft.value = ''
  store.roomSearch = ''
  store.userSearch = ''
  store.roomPublic = ''
  void store.loadRooms(0).then(writeQuery)
}

function setPublic(value: string) {
  store.roomPublic = value
  void store.loadRooms(0).then(writeQuery)
}

function prevPage() {
  void store.loadRooms(Math.max(0, store.rooms.offset - ADMIN_PAGE_SIZE)).then(writeQuery)
}

function nextPage() {
  void store.loadRooms(store.rooms.offset + ADMIN_PAGE_SIZE).then(writeQuery)
}

function askClose(room: AdminRoom) {
  pending.value = { kind: 'close', room }
}

function askUnpublish(room: AdminRoom) {
  pending.value = { kind: 'unpublish', room }
}

function cancelPending() {
  pending.value = null
}

async function confirmPending() {
  const p = pending.value
  if (!p) return
  const ok = p.kind === 'close' ? await store.closeRoom(p.room) : await store.unpublishRoom(p.room)
  if (ok) {
    pending.value = null
    // 列表已被 store 重新拉过，把新的 offset 同步回 URL。
    writeQuery()
  }
}

function shortTime(iso: string): string {
  if (!iso) return '—'
  return iso.replace('T', ' ').slice(0, 16)
}

/** 房间状态的单一结论（多个布尔字段压成一句话，避免界面上一堆互相矛盾的徽标）。 */
function stateText(r: AdminRoom): string {
  if (!r.inMemory) return '已关闭'
  if (r.hostOnline) return '主播在线'
  if (r.hostOffline) return '主播宽限期'
  return '无主播'
}

function stateClass(r: AdminRoom): string {
  if (!r.inMemory) return 'closed'
  if (r.hostOnline) return 'live'
  if (r.hostOffline) return 'grace'
  return 'idle'
}
</script>

<template>
  <div class="page">
    <main class="body">
      <header class="head">
        <h1 class="title">管理端 · 房间</h1>
        <nav class="switcher" aria-label="管理端区块">
          <RouterLink class="tab" :to="{ name: 'admin' }">用户</RouterLink>
          <RouterLink class="tab on" :to="{ name: 'admin-rooms' }">房间</RouterLink>
          <RouterLink class="tab" :to="{ name: 'admin-ops' }">运维</RouterLink>
        </nav>
      </header>

      <!-- 整页结论由服务端裁决（I3）：403 给首页出口，401 给重新登录出口 -->
      <section v-if="store.isDenied" class="denied" role="alert">
        <h2 class="denied-head">
          <BrandIcon name="alert-triangle" decorative :size="16" />
          <template v-if="store.denied === 'forbidden'">无权限访问管理端</template>
          <template v-else-if="store.denied === 'unauthenticated'">登录状态已失效</template>
          <template v-else>管理端暂时不可用</template>
        </h2>
        <p class="denied-text">{{ store.deniedText || '服务端未返回说明。' }}</p>
        <div class="denied-acts">
          <RouterLink
            v-if="store.denied === 'unauthenticated'"
            class="primary"
            :to="{ name: 'login', query: { redirect: '/admin/rooms' } }"
          >
            重新登录
          </RouterLink>
          <RouterLink v-else class="primary" :to="{ name: 'home' }">返回首页</RouterLink>
          <button type="button" class="ghost" @click="loadFromQuery()">重试</button>
        </div>
      </section>

      <template v-else>
        <form class="bar" role="search" @submit.prevent="submitSearch">
          <label class="sr" for="admin-room-search">搜索房间</label>
          <input
            id="admin-room-search"
            v-model="searchDraft"
            type="search"
            placeholder="按房间码 / 标题搜索"
            data-testid="admin-room-search"
          />
          <button type="submit" class="primary" :disabled="store.rooms.loading">搜索</button>
          <button
            v-if="store.roomSearch || store.roomPublic"
            type="button"
            class="ghost"
            data-testid="admin-room-clear"
            @click="clearFilters"
          >
            清除
          </button>
        </form>

        <!-- 公开性筛选：三态对应后端的三态口径（'' / true / false），
             不传非法值——后端对非法值返回 400，而"筛错却看到全量"最难被发现。 -->
        <div class="filters" role="group" aria-label="公开性筛选">
          <button type="button" :class="{ on: store.roomPublic === '' }" @click="setPublic('')">全部</button>
          <button type="button" :class="{ on: store.roomPublic === 'true' }" @click="setPublic('true')">公开</button>
          <button type="button" :class="{ on: store.roomPublic === 'false' }" @click="setPublic('false')">未公开</button>
        </div>

        <p v-if="store.notice" class="ok" role="status" data-testid="admin-room-notice">{{ store.notice }}</p>
        <p v-if="store.noticeError" class="alert" role="alert" data-testid="admin-room-notice-error">
          {{ store.noticeError }}
        </p>

        <section v-if="pending" class="confirm" role="alertdialog" aria-labelledby="room-confirm-head" data-testid="admin-room-confirm">
          <h2 id="room-confirm-head" class="confirm-head">
            <BrandIcon name="alert-triangle" decorative :size="15" />
            <template v-if="pending.kind === 'close'">确认强制关闭</template>
            <template v-else>确认下架</template>
          </h2>
          <p class="confirm-text">
            <template v-if="pending.kind === 'close'">
              将强制关闭房间 <strong class="mono">{{ pending.room.roomId }}</strong>（{{ pending.room.title || '未命名' }}）。
              房内 {{ pending.room.memberCount }} 人的连接会被立刻断开，该房间码随即失效。此操作不可撤销。
            </template>
            <template v-else>
              将把房间 <strong class="mono">{{ pending.room.roomId }}</strong>（{{ pending.room.title || '未命名' }}）从公开列表下架。
              房间本身仍在运行，房主仍可用房间码邀请，但它不再出现在「公开的房间」里。
            </template>
          </p>
          <div class="confirm-acts">
            <button type="button" class="primary" :disabled="store.busyKey !== ''" data-testid="admin-room-confirm-yes" @click="confirmPending">
              确认
            </button>
            <button type="button" class="ghost" :disabled="store.busyKey !== ''" @click="cancelPending">取消</button>
          </div>
        </section>

        <p v-if="store.booting || store.rooms.loading" class="dim">加载中…</p>
        <p v-else-if="store.rooms.error" class="alert" role="alert">{{ store.rooms.error }}</p>
        <p v-else-if="rooms.length === 0" class="dim">
          {{ store.roomSearch || store.roomPublic ? '没有匹配的房间。' : '当前没有任何房间。' }}
        </p>

        <template v-else>
          <div class="table-wrap">
            <table class="table">
              <thead>
                <tr>
                  <th scope="col">房间</th>
                  <th scope="col">状态</th>
                  <th scope="col">公开性</th>
                  <th scope="col">人数 / 深度</th>
                  <th scope="col">最近活跃</th>
                  <th scope="col">操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in rooms" :key="r.roomId">
                  <td>
                    <span class="mono rid">{{ r.roomId }}</span>
                    <span class="rtitle">{{ r.title || '未命名房间' }}</span>
                    <span v-if="r.metadataMissing" class="badge dim">库中无元数据</span>
                  </td>
                  <td>
                    <span class="badge" :class="stateClass(r)">{{ stateText(r) }}</span>
                  </td>
                  <td>
                    <span v-if="!r.inMemory" class="badge dim">—</span>
                    <span v-else class="badge" :class="{ pub: r.isPublic }">{{ r.isPublic ? '公开' : '未公开' }}</span>
                    <span v-if="r.hasPassword" class="badge dim">有密码</span>
                  </td>
                  <td class="mono dim">{{ r.memberCount }} 人 / 深 {{ r.maxDepth }}</td>
                  <td class="mono dim">{{ shortTime(r.lastSeenAt) }}</td>
                  <td class="acts">
                    <button
                      v-if="r.inMemory"
                      type="button"
                      class="danger"
                      :disabled="store.busyKey !== ''"
                      @click="askClose(r)"
                    >
                      强关
                    </button>
                    <!-- 下架只对"在内存里且当前公开"的房间有意义：历史行没有公开性可下架，
                         已未公开的再点一次是幂等 no-op（后端 200 但不写审计），按钮就不必给。 -->
                    <button
                      v-if="r.inMemory && r.isPublic"
                      type="button"
                      :disabled="store.busyKey !== ''"
                      @click="askUnpublish(r)"
                    >
                      下架
                    </button>
                    <span v-if="!r.inMemory" class="dim small">已销毁，仅历史记录</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <ul class="cards">
            <li v-for="r in rooms" :key="r.roomId" class="card">
              <div class="card-head">
                <span class="mono rid">{{ r.roomId }}</span>
                <span class="badge" :class="stateClass(r)">{{ stateText(r) }}</span>
              </div>
              <p class="card-line">{{ r.title || '未命名房间' }}</p>
              <p class="card-line">
                <span v-if="r.inMemory" class="badge" :class="{ pub: r.isPublic }">{{ r.isPublic ? '公开' : '未公开' }}</span>
                <span v-if="r.hasPassword" class="badge dim">有密码</span>
                <span v-if="r.metadataMissing" class="badge dim">库中无元数据</span>
              </p>
              <p class="mono dim card-line">{{ r.memberCount }} 人 / 深 {{ r.maxDepth }} · {{ shortTime(r.lastSeenAt) }}</p>
              <div class="card-acts">
                <button v-if="r.inMemory" type="button" class="danger" :disabled="store.busyKey !== ''" @click="askClose(r)">强关</button>
                <button
                  v-if="r.inMemory && r.isPublic"
                  type="button"
                  :disabled="store.busyKey !== ''"
                  @click="askUnpublish(r)"
                >
                  下架
                </button>
                <span v-if="!r.inMemory" class="dim small">已销毁，仅历史记录</span>
              </div>
            </li>
          </ul>

          <nav class="pager" aria-label="分页">
            <button type="button" :disabled="!store.hasPrev(store.rooms) || store.rooms.loading" @click="prevPage">上一页</button>
            <span class="dim mono">{{ store.rooms.offset + 1 }}–{{ store.rooms.offset + rooms.length }} / {{ total }}</span>
            <button type="button" :disabled="!store.hasNext(store.rooms) || store.rooms.loading" @click="nextPage">下一页</button>
          </nav>
        </template>
      </template>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(1120px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 14px; }
.head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(28px, 5vw, 42px); }
.switcher { display: flex; gap: 2px; }
.tab { padding: 6px 16px; color: var(--dim); border-bottom: 2px solid transparent; font-size: 13px; }
.tab:hover { color: var(--text); text-decoration: none; }
.tab.on { color: var(--text); border-bottom-color: var(--accent); }

.sr { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; border: 0; }

.bar { display: flex; gap: 8px; flex-wrap: wrap; }
.bar input { flex: 1; min-width: 200px; }
.filters { display: flex; gap: 2px; }
.filters button { padding: 5px 14px; border: 1px solid var(--line); color: var(--dim); font-size: 12px; }
.filters button.on { color: var(--text); border-color: var(--accent); background: rgba(180, 108, 255, 0.12); }

button.primary { padding: 8px 18px; background: var(--accent); color: var(--accent-ink); font-weight: 700; }
button.ghost { padding: 8px 16px; border: 1px solid var(--line); color: var(--dim); }
button.ghost:hover { color: var(--text); border-color: var(--accent); }
button.danger { color: var(--danger); border-color: rgba(255, 92, 92, 0.4); }
button.danger:hover { color: #ffdada; border-color: var(--danger); }

.ok { margin: 0; padding: 8px 10px; border: 1px solid var(--ok); background: rgba(61, 220, 151, 0.1); font-size: 12px; color: #d6ffe9; }
.alert { margin: 0; padding: 8px 10px; border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.12); font-size: 12px; color: #ffdada; }

.denied { border: 1px solid var(--line); background: var(--surface); padding: 16px 18px; display: flex; flex-direction: column; gap: 8px; }
.denied-head { margin: 0; font-size: 15px; display: flex; align-items: center; gap: 6px; }
.denied-text { margin: 0; font-size: 13px; color: var(--dim); }
.denied-acts { display: flex; gap: 10px; flex-wrap: wrap; }
.denied-acts .primary { display: inline-flex; align-items: center; padding: 8px 18px; background: var(--accent); color: var(--accent-ink); font-weight: 700; }
.denied-acts .primary:hover { text-decoration: none; }

.confirm { border: 1px solid var(--line-strong); background: rgba(180, 108, 255, 0.08); padding: 14px 16px; display: flex; flex-direction: column; gap: 8px; }
.confirm-head { margin: 0; font-size: 14px; display: flex; align-items: center; gap: 6px; }
.confirm-text { margin: 0; font-size: 13px; line-height: 1.6; }
.confirm-acts { display: flex; gap: 10px; flex-wrap: wrap; }

.table-wrap { border: 1px solid var(--line); overflow-x: auto; }
.table { width: 100%; border-collapse: collapse; font-size: 13px; }
.table th, .table td { padding: 10px 12px; text-align: left; border-bottom: 1px solid var(--line); vertical-align: top; }
.table th { font-size: 12px; color: var(--dim); font-weight: 400; white-space: nowrap; }
.table tbody tr:last-child td { border-bottom: 0; }
.mono { font-family: var(--font-mono); font-size: 11px; }
.rid { display: block; color: var(--text); font-size: 12px; }
.rtitle { display: block; font-size: 12px; color: var(--dim); }
.acts { display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
.acts button, .card-acts button { padding: 5px 12px; border: 1px solid var(--line); color: var(--dim); font-size: 12px; }
.acts button:hover, .card-acts button:hover { color: var(--text); border-color: var(--accent); }
.acts button:disabled, .card-acts button:disabled { opacity: 0.45; cursor: not-allowed; }
.small { font-size: 11px; }

.badge { display: inline-block; margin-right: 4px; padding: 1px 6px; border: 1px solid var(--line); font-size: 11px; color: var(--dim); white-space: nowrap; }
.badge.pub { color: var(--ok); border-color: rgba(61, 220, 151, 0.4); }
.badge.live { color: var(--ok); border-color: rgba(61, 220, 151, 0.4); }
.badge.grace { color: var(--glitch-p); border-color: rgba(255, 110, 199, 0.4); }
.badge.idle { color: var(--dim); }
.badge.closed { color: var(--dim); border-style: dashed; }

.cards { display: none; margin: 0; padding: 0; list-style: none; flex-direction: column; gap: 10px; }
.card { border: 1px solid var(--line); background: var(--surface); padding: 12px; display: flex; flex-direction: column; gap: 5px; }
.card-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.card-line { margin: 0; }
.card-acts { display: flex; gap: 6px; flex-wrap: wrap; align-items: center; margin-top: 4px; }

.pager { display: flex; align-items: center; justify-content: center; gap: 14px; padding-top: 4px; }
.pager button { padding: 7px 16px; border: 1px solid var(--line); color: var(--dim); }
.pager button:hover:not(:disabled) { color: var(--text); border-color: var(--accent); }
.pager button:disabled { opacity: 0.4; cursor: not-allowed; }

@media (max-width: 760px) {
  .table-wrap { display: none; }
  .cards { display: flex; }
}
</style>
