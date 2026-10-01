<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import ChatPanel from '../components/ChatPanel.vue'
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
        <span class="badge" :class="{ ok: store.connection === 'open', danger: store.connection === 'closed' }">
          {{ store.connection === 'open' ? '已连接' : store.connection === 'connecting' ? '连接中' : '已断开' }}
        </span>
        <span class="badge" :class="{ host: store.isHost }">{{ store.isHost ? '主播' : '观众' }}</span>
        <TopologyBadge />
      </div>
      <div class="right">
        <span class="muted">{{ store.displayName }}</span>
        <button @click="leave">离开房间</button>
      </div>
    </header>

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
  </div>
</template>

<style scoped>
.room {
  height: 100vh;
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 14px 18px;
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
}

.label {
  font-size: 12px;
}

button.code {
  font-size: 18px;
  letter-spacing: 3px;
  padding: 4px 12px;
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

@media (max-width: 900px) {
  .room-body {
    grid-template-columns: 1fr;
  }
}
</style>
