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
import { readJoin } from '../utils/joinSession'

const props = defineProps<{ roomId: string }>()

const router = useRouter()
const store = useRoomStore()
const copied = ref(false)

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
  store.leaveRoom()
})

function leave() {
  store.leaveRoom()
  void router.push('/')
}

async function copyCode() {
  try {
    await navigator.clipboard.writeText(props.roomId.toUpperCase())
    copied.value = true
    window.setTimeout(() => (copied.value = false), 1500)
  } catch {
    // 剪贴板不可用（非 HTTPS / 权限被拒）时静默忽略，房间码本来就显示在页面上。
  }
}
</script>

<template>
  <div class="room">
    <header class="room-head">
      <div class="left">
        <span class="label muted">房间码</span>
        <button class="code mono" @click="copyCode" :title="copied ? '已复制' : '点击复制'">
          {{ roomId.toUpperCase() }}
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

    <div class="error-bar" v-if="store.lastError">
      <span>{{ store.lastError }}</span>
      <button @click="store.dismissError()">关闭</button>
    </div>
    <div class="error-bar" v-if="store.roomClosed">
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
