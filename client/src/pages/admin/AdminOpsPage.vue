<script setup lang="ts">
/**
 * 管理端 · 运维视图（wayfinder #25）：指标 / 日志 / 审计。
 *
 * 与用户视图（#23）、房间视图（#24）同构：授权 100% 在服务端（I3），
 * 本页只渲染服务端裁决；503/401/403 由 store 的 `classify` 翻成整页结论。
 *
 * 三条刻意的取舍：
 *
 *  1. **轮询只刷指标，不碰日志/审计的分页**。
 *     日志缓冲是"新行插在头部"的（新→旧），用 offset 分页时若边看边自动刷新，
 *     第 2 页会因为头部插入而整体后移 —— 于是同一行在翻页时被看到两次、另一行被
 *     跳过。验收要求"翻页不重复不丢行"，那就必须让**分页快照在翻页期间保持不动**：
 *     自动刷新只作用于指标，日志与审计各自有显式的"刷新"（会把游标重置回第一页）。
 *  2. **不做图表库**（票里明确 out）：指标是几个整数，画成大字比画成图更易读，
 *     也省掉一个依赖与一份 CSP 风险。
 *  3. **队列的"丢弃"必须解释清楚**：这个 `dropped` 属于**事件类**（本该不可丢），
 *     非零就是真的丢了写入；而"快照类可丢"的那条路径根本不经过这个队列。
 *     口径写错会让人把它当成良性计数，正好反了。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import BrandIcon from '../../components/ui/BrandIcon.vue'
import { ADMIN_PAGE_SIZE, useAdminStore } from '../../stores/admin'
import type { AdminAuditRow } from '../../api/admin'

const store = useAdminStore()

/** 自动刷新间隔（秒）。0 = 关闭。只作用于指标。 */
const refreshSec = ref(0)
let timer: number | undefined

const metrics = computed(() => store.metrics.data)
const writeQueue = computed(() => metrics.value.writeQueue ?? {})
const logStats = computed(() => metrics.value.logs ?? {})

/** 指标里没拿到的键显示为「—」而不是 0：0 和"服务端没给"是两件事。 */
function num(v: number | undefined): string {
  return typeof v === 'number' && Number.isFinite(v) ? String(v) : '—'
}

/** 写入队列的 pending 是**瞬时** gauge，其余是进程内累计计数（后端注释里的口径）。 */
const queueRows = computed(() => [
  { label: '待写（瞬时）', value: num(writeQueue.value.pending), hint: '当前队列长度，涨起来说明写库跟不上' },
  { label: '已入队', value: num(writeQueue.value.queued), hint: '累计接受的作业数' },
  { label: '已丢弃', value: num(writeQueue.value.dropped), hint: '队列满 / 已关闭时被丢弃的事件写入（真丢，非零要查）', danger: true },
  { label: '已成功', value: num(writeQueue.value.succeeded), hint: '累计写入成功' },
  { label: '已失败', value: num(writeQueue.value.failed), hint: '重试后仍失败（已记日志）', danger: true },
  { label: '最近耗时', value: writeQueue.value.lastLatencyMs === undefined ? '—' : `${writeQueue.value.lastLatencyMs} ms`, hint: '最近一次写库往返' },
])

async function loadAll(): Promise<void> {
  // 三个面板一次拉齐：运维视角要的是"此刻的全貌"，分开点三次反而更容易漏看。
  await Promise.all([store.loadMetrics(), store.loadLogs(0), store.loadAudit(0)])
}

/** 只刷指标（理由见文件头第 1 条）。 */
function refreshMetricsOnly(): void {
  void store.loadMetrics()
}

function applyTimer(): void {
  if (timer !== undefined) {
    window.clearInterval(timer)
    timer = undefined
  }
  if (refreshSec.value <= 0) return
  timer = window.setInterval(refreshMetricsOnly, refreshSec.value * 1000)
}

function onIntervalChange(e: Event): void {
  const v = Number((e.target as HTMLSelectElement).value)
  refreshSec.value = Number.isFinite(v) ? v : 0
  applyTimer()
}

function prevLogs(): void {
  void store.loadLogs(Math.max(0, store.logs.offset - ADMIN_PAGE_SIZE))
}

function nextLogs(): void {
  void store.loadLogs(store.logs.offset + ADMIN_PAGE_SIZE)
}

/** 日志"刷新"= 回到第一页（新行在头部，留在中间的页会看到错位的内容）。 */
function reloadLogs(): void {
  void store.loadLogs(0)
}

function prevAudit(): void {
  void store.loadAudit(Math.max(0, store.audit.offset - ADMIN_PAGE_SIZE))
}

function nextAudit(): void {
  void store.loadAudit(store.audit.offset + ADMIN_PAGE_SIZE)
}

function reloadAudit(): void {
  void store.loadAudit(0)
}

/** 时间显示：服务端给 RFC3339，这里只把 T 换空格并按本地时区显示到秒。 */
function shortTime(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso.slice(0, 19).replace('T', ' ')
  const p = (n: number): string => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/** 审计的"对象"：类型 + 标识拼一个可读的串（targetType 可能是 user / room）。 */
function auditTarget(row: AdminAuditRow): string {
  const t = row.targetType || '—'
  return row.targetId === '' ? t : `${t}#${row.targetId}`
}

onMounted(() => {
  store.section = 'metrics'
  void loadAll()
  applyTimer()
})

onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer)
  timer = undefined
})
</script>

<template>
  <div class="page">
    <main class="body">
      <header class="head">
        <h1 class="title">管理端 · 运维</h1>
        <nav class="switcher" aria-label="管理端区块">
          <RouterLink class="tab" :to="{ name: 'admin' }">用户</RouterLink>
          <RouterLink class="tab" :to="{ name: 'admin-rooms' }">房间</RouterLink>
          <RouterLink class="tab on" :to="{ name: 'admin-ops' }">运维</RouterLink>
        </nav>
      </header>

      <!-- 整页结论由服务端裁决 -->
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
            :to="{ name: 'login', query: { redirect: '/admin/ops' } }"
          >
            重新登录
          </RouterLink>
          <RouterLink v-else class="primary" :to="{ name: 'home' }">返回首页</RouterLink>
          <button type="button" class="ghost" @click="loadAll()">重试</button>
        </div>
      </section>

      <template v-else>
        <!-- ================= 指标 ================= -->
        <section class="panel" aria-labelledby="ops-metrics">
          <header class="panel-head">
            <h2 id="ops-metrics" class="panel-title">
              <BrandIcon name="temperature" decorative :size="15" />
              运行指标
            </h2>
            <div class="tools">
              <label class="tool-label" for="ops-interval">自动刷新</label>
              <select id="ops-interval" class="tool-select" :value="String(refreshSec)" data-testid="ops-interval" @change="onIntervalChange">
                <option value="0">关闭</option>
                <option value="10">10 秒</option>
                <option value="30">30 秒</option>
                <option value="60">60 秒</option>
              </select>
              <button type="button" class="ghost" :disabled="store.metrics.loading" data-testid="ops-refresh" @click="refreshMetricsOnly()">
                刷新
              </button>
            </div>
          </header>

          <p v-if="store.metrics.error" class="alert" role="alert">{{ store.metrics.error }}</p>

          <div class="cards">
            <div class="card">
              <span class="card-label">连接</span>
              <strong class="card-value" data-testid="ops-connections">{{ num(metrics.connections) }}</strong>
              <span class="card-hint">当前 WS 连接数</span>
            </div>
            <div class="card">
              <span class="card-label">在线用户</span>
              <strong class="card-value" data-testid="ops-online-users">{{ num(metrics.onlineUsers) }}</strong>
              <span class="card-hint">只统计带账号的连接（游客不计）</span>
            </div>
            <div class="card">
              <span class="card-label">房间</span>
              <strong class="card-value" data-testid="ops-rooms">{{ num(metrics.rooms) }}</strong>
              <span class="card-hint">内存里的活跃房间数</span>
            </div>
            <div class="card">
              <span class="card-label">日志缓冲</span>
              <strong class="card-value">{{ num(logStats.kept) }}</strong>
              <span class="card-hint">
                共 {{ num(logStats.total) }} 行，因容量上限丢弃 {{ num(logStats.dropped) }} 行
              </span>
            </div>
          </div>

          <!-- 写队列：把"瞬时"与"累计"分开列，并置顶那条解释 -->
          <h3 class="sub-title">写队列（事件类：单写协程 + 有界队列）</h3>
          <ul class="queue">
            <li v-for="row in queueRows" :key="row.label">
              <span class="q-label">{{ row.label }}</span>
              <strong class="q-value" :class="{ danger: row.danger }">{{ row.value }}</strong>
              <span class="q-hint">{{ row.hint }}</span>
            </li>
          </ul>

          <!-- 口径说明：这一段是票里点名的要求，写错方向就把关键信号解释反了 -->
          <p class="note" data-testid="ops-queue-note">
            <strong>怎么读这个「已丢弃」</strong>：它数的是<strong>事件类</strong>写入（消息、好友关系、元数据、封禁与审计）
            在队列满时被丢掉的作业 —— 那一类本该<strong>不可丢</strong>，所以非零就是一个要立刻查的信号。
            <strong>快照类可丢</strong>（成员数 / 最大深度 / 播放进度）走的是另一条周期性 ticker 路径，
            <strong>不经过这个队列</strong>，因此它的丢弃不会出现在这里。
            切片队列的指标目前不在 <code>/api/admin/metrics</code> 里，本页不臆造这一项。
          </p>
        </section>

        <!-- ================= 日志 ================= -->
        <section class="panel" aria-labelledby="ops-logs">
          <header class="panel-head">
            <h2 id="ops-logs" class="panel-title">
              <BrandIcon name="tips" decorative :size="15" />
              服务端日志
              <span class="count dim">共 {{ store.logs.total }} 行</span>
            </h2>
            <div class="tools">
              <button type="button" class="ghost" :disabled="store.logs.loading" data-testid="ops-logs-refresh" @click="reloadLogs()">
                刷新
              </button>
            </div>
          </header>

          <p class="note small">
            环形缓冲，新→旧。<strong>只读文本</strong>：服务端在写入侧已去掉控制字符，前端原样插值、不做任何 HTML 加工。
          </p>

          <p v-if="store.logs.error" class="alert" role="alert">{{ store.logs.error }}</p>
          <p v-else-if="store.logs.loading && store.logs.items.length === 0" class="dim">加载中…</p>
          <p v-else-if="store.logs.items.length === 0" class="dim">还没有日志行。</p>

          <ol v-else class="logs" data-testid="ops-logs">
            <li v-for="(line, i) in store.logs.items" :key="`${store.logs.offset}-${i}`" class="log-line">{{ line }}</li>
          </ol>

          <nav class="pager" aria-label="日志分页">
            <button type="button" :disabled="!store.hasPrev(store.logs) || store.logs.loading" @click="prevLogs">上一页</button>
            <span class="dim mono">{{ store.logs.offset + 1 }}–{{ store.logs.offset + store.logs.items.length }} / {{ store.logs.total }}</span>
            <button type="button" :disabled="!store.hasNext(store.logs) || store.logs.loading" @click="nextLogs">下一页</button>
          </nav>
        </section>

        <!-- ================= 审计 ================= -->
        <section class="panel" aria-labelledby="ops-audit">
          <header class="panel-head">
            <h2 id="ops-audit" class="panel-title">
              <BrandIcon name="shield-done" decorative :size="15" />
              管理端审计
            </h2>
            <div class="tools">
              <button type="button" class="ghost" :disabled="store.audit.loading" data-testid="ops-audit-refresh" @click="reloadAudit()">
                刷新
              </button>
            </div>
          </header>

          <p class="note small">
            每次写操作一行：谁、何时、对谁、改了什么、来源 IP。审计表<strong>没有</strong> total
            （服务端刻意不为翻页做一次全表 COUNT），所以"还有下一页"按本页是否满页判断。
          </p>

          <p v-if="store.audit.error" class="alert" role="alert">{{ store.audit.error }}</p>
          <p v-else-if="store.audit.loading && store.audit.items.length === 0" class="dim">加载中…</p>
          <p v-else-if="store.audit.items.length === 0" class="dim">还没有审计记录。</p>

          <template v-else>
            <div class="table-wrap">
              <table class="table">
                <thead>
                  <tr>
                    <th scope="col">时间</th>
                    <th scope="col">操作者</th>
                    <th scope="col">动作</th>
                    <th scope="col">对象</th>
                    <th scope="col">来源 IP</th>
                    <th scope="col">明细</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="row in store.audit.items" :key="row.id">
                    <td class="mono">{{ shortTime(row.createdAt) }}</td>
                    <td class="mono">#{{ row.actorId }}</td>
                    <td class="action">{{ row.action }}</td>
                    <td class="mono">{{ auditTarget(row) }}</td>
                    <td class="mono">{{ row.ip || '—' }}</td>
                    <td class="detail mono">{{ row.detail || '—' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>

            <ul class="audit-cards">
              <li v-for="row in store.audit.items" :key="row.id" class="audit-card">
                <div class="ac-head">
                  <span class="action">{{ row.action }}</span>
                  <span class="mono dim">{{ shortTime(row.createdAt) }}</span>
                </div>
                <p class="ac-line mono">操作者 #{{ row.actorId }} · {{ auditTarget(row) }}</p>
                <p class="ac-line mono dim">来源 IP {{ row.ip || '—' }}</p>
                <p class="ac-line detail mono">{{ row.detail || '—' }}</p>
              </li>
            </ul>
          </template>

          <nav class="pager" aria-label="审计分页">
            <button type="button" :disabled="!store.hasPrev(store.audit) || store.audit.loading" @click="prevAudit">上一页</button>
            <span class="dim mono">
              {{ store.audit.offset + 1 }}–{{ store.audit.offset + store.audit.items.length }}
            </span>
            <button type="button" :disabled="!store.hasNext(store.audit) || store.audit.loading" @click="nextAudit">下一页</button>
          </nav>
        </section>
      </template>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(1120px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 16px; }
.head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(28px, 5vw, 42px); }
.switcher { display: flex; gap: 2px; }
.tab { padding: 6px 16px; color: var(--dim); border-bottom: 2px solid transparent; font-size: 13px; }
.tab:hover { color: var(--text); text-decoration: none; }
.tab.on { color: var(--text); border-bottom-color: var(--accent); }

.panel { border: 1px solid var(--line); background: var(--surface); padding: 14px 16px 16px; display: flex; flex-direction: column; gap: 10px; }
.panel-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
.panel-title { margin: 0; font-size: 14px; display: flex; align-items: center; gap: 6px; }
.count { font-size: 12px; font-weight: 400; margin-left: 4px; }
.sub-title { margin: 4px 0 0; font-size: 12px; color: var(--dim); font-weight: 400; }

.tools { display: flex; align-items: center; gap: 8px; flex-wrap: nowrap; }
.tool-label { font-size: 12px; color: var(--dim); white-space: nowrap; }
.tool-select { padding: 5px 8px; border: 1px solid var(--line); background: var(--bg-mid); color: var(--text); font-size: 12px; }

/* nowrap：面板标题右侧空间紧张时，按钮不该被压成"刷/新"两行 */
button.ghost { padding: 6px 14px; border: 1px solid var(--line); color: var(--dim); font-size: 12px; white-space: nowrap; }
button.ghost:hover:not(:disabled) { color: var(--text); border-color: var(--accent); }
button.ghost:disabled { opacity: 0.45; cursor: not-allowed; }

.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 10px; }
.card { border: 1px solid var(--line); background: var(--bg-mid); padding: 10px 12px; display: flex; flex-direction: column; gap: 3px; }
.card-label { font-size: 11px; color: var(--dim); }
.card-value { font-family: var(--font-mono); font-size: 24px; line-height: 1.1; }
.card-hint { font-size: 11px; color: var(--dim); line-height: 1.5; }

.queue { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 6px; }
.queue li { display: grid; grid-template-columns: 96px 84px 1fr; gap: 8px; align-items: baseline; font-size: 12px; }
.q-label { color: var(--dim); }
.q-value { font-family: var(--font-mono); font-size: 13px; }
.q-value.danger { color: var(--danger); }
.q-hint { color: var(--dim); line-height: 1.5; }

.note { margin: 0; font-size: 12px; color: var(--dim); line-height: 1.7; border-top: 1px dashed var(--line); padding-top: 8px; }
.note.small { font-size: 11px; }
.note code { font-family: var(--font-mono); font-size: 11px; color: var(--text); }

.logs { margin: 0; padding: 8px 10px; list-style: none; border: 1px solid var(--line); background: rgba(0, 0, 0, 0.35); max-height: 420px; overflow-y: auto; display: flex; flex-direction: column; gap: 2px; }
.log-line { font-family: var(--font-mono); font-size: 11px; line-height: 1.6; color: #cfc2e6; white-space: pre-wrap; word-break: break-word; }

.table-wrap { border: 1px solid var(--line); overflow-x: auto; }
.table { width: 100%; border-collapse: collapse; font-size: 12px; }
.table th, .table td { padding: 8px 10px; text-align: left; border-bottom: 1px solid var(--line); vertical-align: top; }
.table th { font-size: 11px; color: var(--dim); font-weight: 400; white-space: nowrap; }
.table tbody tr:last-child td { border-bottom: 0; }
.action { color: var(--accent); font-family: var(--font-mono); font-size: 11px; white-space: nowrap; }
.detail { max-width: 320px; word-break: break-word; }
.mono { font-family: var(--font-mono); font-size: 11px; }

.audit-cards { display: none; margin: 0; padding: 0; list-style: none; flex-direction: column; gap: 8px; }
.audit-card { border: 1px solid var(--line); background: var(--bg-mid); padding: 10px 12px; display: flex; flex-direction: column; gap: 4px; }
.ac-head { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
.ac-line { margin: 0; word-break: break-word; }

.pager { display: flex; align-items: center; justify-content: center; gap: 14px; }
.pager button { padding: 6px 14px; border: 1px solid var(--line); color: var(--dim); font-size: 12px; }
.pager button:hover:not(:disabled) { color: var(--text); border-color: var(--accent); }
.pager button:disabled { opacity: 0.4; cursor: not-allowed; }

.alert { margin: 0; padding: 8px 10px; border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.12); font-size: 12px; color: #ffdada; }

.denied { border: 1px solid var(--line); background: var(--surface); padding: 16px 18px; display: flex; flex-direction: column; gap: 8px; }
.denied-head { margin: 0; font-size: 15px; display: flex; align-items: center; gap: 6px; }
.denied-text { margin: 0; font-size: 13px; color: var(--dim); }
.denied-acts { display: flex; gap: 10px; flex-wrap: wrap; }
.denied-acts .primary { display: inline-flex; align-items: center; padding: 8px 18px; background: var(--accent); color: var(--accent-ink); font-weight: 700; }
.denied-acts .primary:hover { text-decoration: none; }

/* 窄屏：表格换卡片（表格在 375px 上必然横向滚动，把"来源 IP"挤出视野）；
   写队列的三列也收成一列，避免标签与值被压成两行错位。 */
@media (max-width: 760px) {
  .table-wrap { display: none; }
  .audit-cards { display: flex; }
  .queue li { grid-template-columns: 88px 1fr; }
  .q-hint { grid-column: 1 / -1; }
}
</style>
