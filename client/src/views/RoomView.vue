<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import ChatPanel from '../components/ChatPanel.vue'
import DiagnosticsDrawer from '../components/DiagnosticsDrawer.vue'
import HostPanel from '../components/HostPanel.vue'
import MemberList from '../components/MemberList.vue'
import PlayerStage from '../components/PlayerStage.vue'
import TopologyBadge from '../components/TopologyBadge.vue'
import { newClientId, useRoomStore } from '../stores/room'
import type { JoinCredentials } from '../stores/room'
import { copyText } from '../utils/clipboard'
import { readJoin } from '../utils/joinSession'

const props = defineProps<{ roomId: string }>()

const router = useRouter()
const store = useRoomStore()
/**
 * 复制反馈：''=未复制 / ok=已复制 / failed=复制失败。
 *
 * 之前这里只调异步 Clipboard API 且在 catch 里静默忽略：内网穿透的 **http 域名**属于
 * 非安全上下文，那个 API 根本不存在，于是"点了没反应"，用户只能反复点。
 * 现在统一走 utils/clipboard.copyText()（失败退回 textarea + execCommand，http 下可用），
 * 并且真的失败时必须说出来，而不是继续装成功。
 */
const copyState = ref<'' | 'ok' | 'failed'>('')
let copyResetTimer: number | undefined

/** 连接状态文案：把"重连"这件事说清楚，否则用户只会看到一个红点。 */
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

const connectionClass = computed(() => ({
  ok: store.connection === 'open' && store.joined,
  danger: store.connection === 'closed',
}))

/** 信令没就绪时给一条明确的进度说明，而不是让页面看起来"卡住了"。 */
const linkHint = computed(() => {
  if (store.connection === 'closed') {
    if (store.isHost) {
      // 主播断线：说清楚"房间还在、还有多久"，否则用户会以为房间已经没了而直接关页面。
      return (
        '与服务端的信令连接断了（网络抖动、服务端重启都会这样）。已自动重连；' +
        `服务端会为本房间保留约 ${store.hostGraceSeconds} 秒，期间重连成功即自动回到同一房间码，` +
        '播放状态与已发布的分片索引都不会丢。'
      )
    }
    return '与服务端的信令连接断了（服务端重启、网络抖动都会这样）。已自动重连，重连成功后会重新进房并继续跟随主播。'
  }
  if (store.connection === 'connecting') {
    return '正在连接服务端信令…'
  }
  if (store.connection === 'open' && !store.joined) {
    return '信令已连上，正在加入房间…'
  }
  return ''
})

onMounted(() => {
  // 直接输 URL 进来没有凭据（密码/昵称/角色），回首页重新走流程。
  const stored = readJoin(props.roomId)
  if (!stored) {
    void router.replace('/')
    return
  }

  const credentials: JoinCredentials = {
    roomId: props.roomId.toUpperCase(),
    clientId: newClientId(),
    displayName: stored.displayName || '匿名观众',
    role: stored.role === 'host' ? 'host' : 'viewer',
    password: stored.password ?? '',
  }
  store.enterRoom(credentials)
})

onBeforeUnmount(() => {
  if (copyResetTimer !== undefined) window.clearTimeout(copyResetTimer)
  store.leaveRoom()
})

function leave() {
  store.leaveRoom()
  void router.push('/')
}

async function copyCode() {
  // copyText 自己兜住所有异常并返回布尔值，这里只负责把结果说清楚。
  const done = await copyText(props.roomId.toUpperCase())
  copyState.value = done ? 'ok' : 'failed'
  if (copyResetTimer !== undefined) window.clearTimeout(copyResetTimer)
  // 失败提示比成功提示多留一点：用户需要时间看完并改为手动选中。
  copyResetTimer = window.setTimeout(() => (copyState.value = ''), done ? 1500 : 2000)
}
</script>

<template>
  <div class="room">
    <header class="room-head">
      <div class="left">
        <span class="label muted">房间码</span>
        <!--
          点击复制，但房间码本身仍是可选中文本（.code-text 显式 user-select: text）：
          复制兜底也失败时，用户可以直接手动选中再 Ctrl+C，而不是"点了没反应"。
        -->
        <button
          class="code mono"
          :class="{ copied: copyState === 'ok', failed: copyState === 'failed' }"
          data-testid="room-code"
          :title="copyState === 'ok' ? '已复制' : '点击复制房间码'"
          @click="copyCode"
        >
          <span class="code-text">{{ roomId.toUpperCase() }}</span>
          <span class="copy-state" v-if="copyState === 'ok'" data-testid="room-code-copied">已复制</span>
          <span class="copy-state failed" v-else-if="copyState === 'failed'" data-testid="room-code-copy-failed">
            复制失败，请手动选中
          </span>
        </button>
        <span class="badge" :class="connectionClass">{{ connectionText }}</span>
        <span class="badge" :class="{ host: store.isHost }">{{ store.isHost ? '主播' : '观众' }}</span>
        <TopologyBadge />
      </div>
      <div class="right">
        <span class="muted">{{ store.displayName }}</span>
        <button @click="leave">离开房间</button>
      </div>
    </header>

    <div class="link-bar" v-if="linkHint">
      <span>{{ linkHint }}</span>
    </div>

    <!--
      主播离线等待态：判据是"已进房 + 成员表里没有 host"（契约 2）。
      倒计时只是给用户的预期管理，**权威是服务端的 room-closed**：收到它就以它给的原因收场。
    -->
    <div class="wait-bar" v-if="store.hostOffline" data-testid="host-offline-bar">
      <span>{{ store.hostOfflineText }}</span>
    </div>

    <!-- 观众侧等待主播重建房间（ROOM_NOT_FOUND 后的有界重试） -->
    <div class="wait-bar" v-if="store.resumeNotice" data-testid="room-resume-bar">
      <span>{{ store.resumeNotice }}</span>
    </div>

    <!-- 不可自动恢复：说清楚要做什么，而不是停在"房间不存在"上 -->
    <div class="error-bar" v-if="store.roomUnrecoverable" data-testid="room-unrecoverable-bar">
      <span>{{ store.roomUnrecoverable }}</span>
      <button @click="leave">返回首页</button>
    </div>

    <div class="error-bar" v-if="store.lastError">
      <span>{{ store.lastError }}</span>
      <button @click="store.dismissError()">关闭</button>
    </div>
    <div class="error-bar" v-if="store.roomClosed" data-testid="room-closed-bar">
      <span>{{ store.roomClosed }}</span>
      <button @click="leave">返回首页</button>
    </div>

    <main class="room-body">
      <section class="stage-col">
        <HostPanel v-if="store.isHost" />
        <PlayerStage />
      </section>
      <aside class="side-col">
        <MemberList />
        <ChatPanel />
      </aside>
    </main>

    <!-- 排障入口：默认收起，展开后是只读日志 + 复制报告。 -->
    <DiagnosticsDrawer />
  </div>
</template>

<style scoped>
.room {
  height: 100vh;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px 16px;
}

.room-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.room-head .left,
.room-head .right {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  min-width: 0;
}

.room-head .left {
  /* 拓扑徽标很长：让它自己收缩省略，不要把右侧挤到第二行。 */
  flex: 1 1 420px;
  min-width: 0;
}

.label {
  font-size: 12px;
}

button.code {
  font-size: 18px;
  letter-spacing: 3px;
  padding: 4px 12px;
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

/* 房间码本体必须能被手动选中：复制兜底失败时这是最后一条路。 */
button.code .code-text {
  user-select: text;
  -webkit-user-select: text;
}

button.code.copied {
  border-color: var(--ok);
}

button.code.failed {
  border-color: var(--danger);
}

button.code .copy-state {
  font-size: 12px;
  letter-spacing: 0;
  color: var(--ok);
}

button.code .copy-state.failed {
  color: var(--danger);
}

/* 主播离线 / 等待重建：与"连接说明"同一形状，但用等待色区分于错误。 */
.wait-bar {
  background: rgba(240, 178, 60, 0.12);
  border: 1px solid rgba(240, 178, 60, 0.55);
  border-radius: 8px;
  padding: 6px 12px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text);
}

/* 连接/重连说明：只在信令没就绪时出现，不占常驻空间。 */
.link-bar {
  background: rgba(0, 161, 214, 0.12);
  border: 1px solid var(--accent);
  border-radius: 8px;
  padding: 6px 12px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text);
}

.room-body {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 340px;
  gap: 14px;
}

.stage-col {
  min-width: 0;
  /* 左列自己滚：主播展开「切片面板 + 教程」时能有几百甚至上千像素高，
     不让它顶破定高的 .room（否则内容会盖到页脚的状态条上）。 */
  min-height: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.side-col {
  min-height: 0;
  display: grid;
  grid-template-rows: minmax(120px, auto) minmax(0, 1fr);
  gap: 12px;
}

/* 1280 档：侧栏收窄一点，给播放器与控制区留出空间。 */
@media (max-width: 1280px) {
  .room {
    padding: 10px 12px;
  }

  .room-body {
    grid-template-columns: minmax(0, 1fr) 300px;
    gap: 12px;
  }
}

/* 900 档：单列堆叠，整页可以滚动（固定 100vh 会把聊天压扁）。 */
@media (max-width: 900px) {
  .room {
    height: auto;
    min-height: 100vh;
  }

  .room-body {
    grid-template-columns: minmax(0, 1fr);
  }

  .side-col {
    grid-template-rows: none;
    grid-auto-rows: min-content;
  }

  /* 聊天在窄屏下给一个明确高度，成员列表按内容走。 */
  .side-col > :last-child {
    height: 45vh;
    min-height: 240px;
  }
}
</style>
