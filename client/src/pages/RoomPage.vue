<script setup lang="ts">
/**
 * 房间室（/room/:roomId）。看片优先双栏（票 #17）：
 *   桌面 = 左舞台 + 右常驻侧栏（主播控制上、聊天中、成员/拓扑折叠）
 *   375px = 单列：播放器 → 主播控制 → 聊天 → 成员/拓扑
 *
 * 进房装配在 useRoomEntry（硬契约逐字保留）。
 *
 * 契约（票 #12 §8）：
 *   - data-testid="room-code" 内含 .code-text（user-select:text）；
 *   - .code-text 文本与复制内容都 === 大写房间码；复制失败要回退 execCommand；
 *   - 「已复制」/「复制失败，请手动选中」；
 *   - 页面树零 input[type=password]；
 *   - 离开房间 = 纯图标 + aria-label + v-tip，点击调 store.leaveRoom()。
 */
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import MemberList from '../components/ui/MemberList.vue'
import TopologyBadge from '../components/ui/TopologyBadge.vue'
import PlayerStage from '../components/features/room/PlayerStage.vue'
import HostPanel from '../components/features/room/HostPanel.vue'
import ChatPanel from '../components/features/room/ChatPanel.vue'
import { useRoomEntry } from '../components/features/room/useRoomEntry'
import { copyText } from '../utils/clipboard'

const props = defineProps<{ roomId: string }>()

const router = useRouter()
const { store, missingCredential } = useRoomEntry(() => props.roomId)

/** ''=未复制 / ok / failed（旧 RoomView 的两套状态机之一，保留）。 */
const copyState = ref<'' | 'ok' | 'failed'>('')
let copyResetTimer: ReturnType<typeof setTimeout> | undefined

const code = computed(() => props.roomId.toUpperCase())

const connectionText = computed(() => {
  switch (store.connection) {
    case 'open':
      return store.joined ? '已连接' : '已连接，正在进房…'
    case 'connecting':
      return '连接中…'
    case 'closed':
      return '已断开，正在自动重连…'
    default:
      return '未连接'
  }
})

/** 信令未就绪时一句话说明"现在在发生什么 + 正在自动恢复"。 */
const linkHint = computed(() => {
  if (store.connection === 'closed') {
    return store.isHost
      ? `信令断了，正在自动重连；服务端会为本房间保留约 ${store.hostGraceSeconds} 秒，期间重连成功即回到同一房间码。`
      : '信令断了，正在自动重连，成功后会自动重新进房并继续跟随主播。'
  }
  if (store.connection === 'connecting') return '正在连接服务端信令…'
  if (store.connection === 'open' && !store.joined) return '信令已连上，正在加入房间…'
  return ''
})

async function copyCode() {
  const done = await copyText(code.value)
  copyState.value = done ? 'ok' : 'failed'
  if (copyResetTimer !== undefined) clearTimeout(copyResetTimer)
  // 失败提示多留一点：用户需要时间看完并改为手动选中
  copyResetTimer = setTimeout(() => (copyState.value = ''), done ? 1500 : 2000)
}

function leave() {
  store.leaveRoom()
  void router.push('/')
}

onBeforeUnmount(() => {
  if (copyResetTimer !== undefined) clearTimeout(copyResetTimer)
})
</script>

<template>
  <div class="room">
    <header class="room-head">
      <div class="left">
        <span class="label dim">房间码</span>
        <!--
          点击复制，但房间码本身仍是可选中文本：复制兜底也失败时，
          用户可以直接手动选中再 Ctrl+C，而不是"点了没反应"。
        -->
        <button
          class="code mono"
          :class="{ copied: copyState === 'ok', failed: copyState === 'failed' }"
          data-testid="room-code"
          :aria-label="copyState === 'ok' ? '房间码已复制' : '复制房间码'"
          :title="copyState === 'ok' ? '已复制' : '点击复制房间码'"
          @click="copyCode"
        >
          <span class="code-text">{{ code }}</span>
          <BrandIcon
            :name="copyState === 'failed' ? 'alert-triangle' : copyState === 'ok' ? 'check-circle' : 'copy'"
            decorative
            :size="15"
          />
          <span v-if="copyState === 'ok'" class="copy-state" data-testid="room-code-copied">已复制</span>
          <span v-else-if="copyState === 'failed'" class="copy-state failed" data-testid="room-code-copy-failed">
            复制失败，请手动选中
          </span>
        </button>

        <span class="badge" :class="{ ok: store.connection === 'open' && store.joined, danger: store.connection === 'closed' }">
          {{ connectionText }}
        </span>
        <span class="badge" :class="{ host: store.isHost }">{{ store.isHost ? '主播' : '观众' }}</span>
        <span v-if="store.displayName" class="dim">{{ store.displayName }}</span>
      </div>

      <div class="right">
        <!-- 离开房间：纯图标（票 #12 裁决），aria-label + v-tip 承担可访问名。
             顶栏与分区导航由全局外壳（App.vue）常驻，本页不再内嵌 AppHeader。 -->
        <button class="leave" aria-label="离开房间" v-tip="'离开房间'" @click="leave">
          <BrandIcon name="logout" decorative :size="17" />
        </button>
      </div>
    </header>

    <p v-if="linkHint" class="link-bar">
      <span>{{ linkHint }}</span>
    </p>
    <div v-if="store.hostOffline" class="wait-bar" data-testid="host-offline-bar">
      <span>{{ store.hostOfflineText }}</span>
    </div>
    <div v-if="store.resumeNotice" class="wait-bar" data-testid="room-resume-bar">
      <span>{{ store.resumeNotice }}</span>
    </div>
    <div v-if="store.roomUnrecoverable" class="error-bar" data-testid="room-unrecoverable-bar">
      <span>{{ store.roomUnrecoverable }}</span>
      <button @click="leave">返回首页</button>
    </div>
    <div v-if="store.lastError" class="error-bar">
      <span>{{ store.lastError }}</span>
      <button @click="store.dismissError()">关闭</button>
    </div>
    <div v-if="store.roomClosed" class="error-bar" data-testid="room-closed-bar">
      <span>{{ store.roomClosed }}</span>
      <button @click="leave">返回首页</button>
    </div>

    <main class="room-body">
      <section class="stage-col">
        <p v-if="missingCredential" class="dim">没有进房凭据。请回首页用房间码进入（不需要账号）。</p>
        <PlayerStage />
      </section>
      <aside class="side-col">
        <HostPanel v-if="store.isHost" />
        <ChatPanel />
        <TopologyBadge />
        <MemberList />
      </aside>
    </main>
  </div>
</template>

<style scoped>
.room {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

.room-head {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 18px;
  border-bottom: 1px solid var(--line);
  flex-wrap: wrap;
}

.left {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}

.label {
  font-size: 12px;
}

.code {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  border: 1px solid var(--line);
  background: var(--surface);
  color: var(--text);
  font-size: 14px;
  letter-spacing: 0.08em;
  transition: border-color var(--dur-micro) var(--ease);
}

.code:hover {
  border-color: var(--accent);
}

.code-text {
  user-select: text;
}

.copy-state {
  font-size: 11px;
  color: var(--ok);
  font-family: var(--font-ui);
  letter-spacing: 0;
}

.copy-state.failed {
  color: var(--glitch-p);
}

.badge {
  padding: 2px 8px;
  border: 1px solid var(--line);
  font-size: 11px;
  color: var(--dim);
}

.badge.ok {
  border-color: var(--ok);
  color: var(--ok);
}

.badge.danger {
  border-color: var(--danger);
  color: var(--danger);
}

.badge.host {
  border-color: var(--glitch-p);
  color: var(--glitch-p);
}

.right {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
}

.leave {
  width: 36px;
  height: 36px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-pill);
  border: 1px solid transparent;
  color: var(--dim);
  transition: color var(--dur-micro) var(--ease), border-color var(--dur-micro) var(--ease);
}

.leave:hover {
  color: var(--glitch-r);
  border-color: var(--glitch-r);
}

.link-bar,
.wait-bar,
.error-bar {
  margin: 0;
  padding: 8px 18px;
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 10px;
}

.link-bar {
  background: rgba(180, 108, 255, 0.1);
  color: var(--dim);
}

.wait-bar {
  background: rgba(255, 200, 0, 0.1);
}

.error-bar {
  background: rgba(255, 92, 92, 0.12);
}

.error-bar button {
  margin-left: auto;
  padding: 4px 12px;
  border: 1px solid var(--danger);
  color: #ffdada;
}

.room-body {
  flex: 1;
  display: grid;
  grid-template-columns: 1fr minmax(320px, 380px);
  gap: 16px;
  padding: 16px 18px 28px;
  align-items: start;
}

.stage-col {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.side-col {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}

/* 375px 单列：播放器 → 主播控制 → 聊天 → 成员/拓扑（DOM 顺序即视觉顺序） */
@media (max-width: 760px) {
  .room-body {
    grid-template-columns: 1fr;
  }
}
</style>
