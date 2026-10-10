<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
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

/** 站点默认标题（`index.html` 里那一份）；离开房间时还原。 */
const DEFAULT_TITLE = '月喵 · 一起看'

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

/** 分享反馈：与复制房间码同一套状态机（分享也是往剪贴板里写东西，失败的原因完全一样）。 */
const shareState = ref<'' | 'ok' | 'failed'>('')
let shareResetTimer: number | undefined

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

/**
 * 信令没就绪时给一条明确的进度说明，而不是让页面看起来"卡住了"。
 *
 * 只留**一句话**：重连机制、宽限期、为什么会断、卡顿怎么排查这些解释都在 `/help#faq`，
 * 核心页不再堆成段说明（要点仍是"现在发生了什么 + 正在自动恢复"）。
 */
const linkHint = computed(() => {
  if (store.connection === 'closed') {
    if (store.isHost) {
      // 主播断线：说清楚"房间还在、还有多久"，否则用户会以为房间已经没了而直接关页面。
      return `与服务端的信令断了，正在自动重连；服务端会为本房间保留约 ${store.hostGraceSeconds} 秒，期间重连成功即回到同一房间码。`
    }
    return '与服务端的信令断了，正在自动重连，成功后会自动重新进房并继续跟随主播。'
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
  // 房间页标题带上房间码：多标签页时能一眼分清哪个标签是哪个房间。
  document.title = `月喵 · 房间 ${props.roomId.toUpperCase()}`

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
    // 主播复位令牌（S-7）：创建房间时下发、存进同一份 join 凭据里。
    // 不带上它，主播断线后在宽限期内就抢不回主播位（服务端会回 HOST_TOKEN_REQUIRED）。
    hostToken: stored.hostToken,
  }
  store.enterRoom(credentials)
})

onBeforeUnmount(() => {
  if (copyResetTimer !== undefined) window.clearTimeout(copyResetTimer)
  if (shareResetTimer !== undefined) window.clearTimeout(shareResetTimer)
  document.title = DEFAULT_TITLE
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

/**
 * 分享：把"房间码 + 网址"一次复制走（免登录进房，所以不需要登录链接）。
 *
 * 为什么不是复制 `/room/<码>` 这个 URL：直接打开那个地址时本机没有进房凭据，
 * 会被路由送回首页 —— 那样的链接是**看起来能进、其实进不去**。
 * 复制一段带房间码的说明文案，对方打开网址输码即可，才是真的能用。
 */
async function share() {
  const code = props.roomId.toUpperCase()
  const text = `月喵房间码 ${code} · 打开 ${window.location.origin} 输入房间码即可进房（不需要账号）`
  const done = await copyText(text)
  shareState.value = done ? 'ok' : 'failed'
  if (shareResetTimer !== undefined) window.clearTimeout(shareResetTimer)
  shareResetTimer = window.setTimeout(() => (shareState.value = ''), done ? 1500 : 2000)
}
</script>

<template>
  <div class="room">
    <header class="room-head">
      <div class="left">
        <span class="label muted">
          <BrandIcon name="ticket" decorative :size="12" />
          房间码
        </span>
        <!--
          点击复制，但房间码本身仍是可选中文本（.code-text 显式 user-select: text）：
          复制兜底也失败时，用户可以直接手动选中再 Ctrl+C，而不是"点了没反应"。
          复制动作由按钮本身承担（图标 + title + aria-label），房间码文字必须留下 —— 它就是内容。
        -->
        <button
          class="code mono"
          :class="{ copied: copyState === 'ok', failed: copyState === 'failed' }"
          data-testid="room-code"
          :aria-label="copyState === 'ok' ? '房间码已复制' : '复制房间码'"
          :title="copyState === 'ok' ? '已复制' : '点击复制房间码'"
          @click="copyCode"
        >
          <span class="code-text">{{ roomId.toUpperCase() }}</span>
          <BrandIcon
            :name="copyState === 'failed' ? 'alert-triangle' : copyState === 'ok' ? 'check-circle' : 'copy'"
            decorative
            :size="15"
          />
          <span class="copy-state" v-if="copyState === 'ok'" data-testid="room-code-copied">已复制</span>
          <span class="copy-state failed" v-else-if="copyState === 'failed'" data-testid="room-code-copy-failed">
            复制失败，请手动选中
          </span>
        </button>

        <!-- 分享：把"房间码 + 网址"一次复制走（免登录进房，所以不需要登录链接）。 -->
        <button
          class="icon-btn"
          :class="{ failed: shareState === 'failed' }"
          :aria-label="shareState === 'ok' ? '分享信息已复制' : '复制房间分享信息（房间码与网址）'"
          v-tip="shareState === 'ok' ? '已复制分享信息' : '复制房间分享信息（房间码与网址）'"
          data-testid="room-share"
          @click="share"
        >
          <BrandIcon
            :name="shareState === 'failed' ? 'alert-triangle' : shareState === 'ok' ? 'check-circle' : 'share'"
            decorative
            :size="16"
          />
        </button>
        <span class="muted small" v-if="shareState === 'failed'">分享信息复制失败，请手动选中房间码</span>

        <span class="badge" :class="connectionClass">{{ connectionText }}</span>
        <span class="badge" :class="{ host: store.isHost }">{{ store.isHost ? '主播' : '观众' }}</span>
        <TopologyBadge />
      </div>
      <div class="right">
        <span class="muted">{{ store.displayName }}</span>
        <!--
          账号区塞进**已有的**这一行（判据：不破坏房间页）。房间页是 height:100vh 的固定布局，
          单开一行顶栏就是从播放器身上抠高度，所以这里用 inline 形态，不新增行。
          「退出」是退出账号（判据⑤ 会回首页），与左边的「离开房间」是两件事。
          inline 形态里已经带了帮助与开源仓库入口，核心页因此不需要再放一段说明文字。
        -->
        <AppHeader variant="inline" />
        <button @click="leave">离开房间</button>
      </div>
    </header>

    <div class="link-bar" v-if="linkHint">
      <span>{{ linkHint }}</span>
      <RouterLink class="bar-link" :to="{ name: 'help', hash: '#faq' }">
        <BrandIcon name="tips" decorative :size="13" />
        重连/卡顿的排查
      </RouterLink>
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
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

/* 纯图标按钮（分享）：点击区靠 padding 撑出来，可访问名走 aria-label / title。 */
button.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 5px;
  background: transparent;
  border-color: transparent;
  color: var(--text-dim);
  line-height: 0;
}

button.icon-btn:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--border);
}

button.icon-btn.failed {
  color: var(--danger);
}

.small {
  font-size: 12px;
}

/* 说明条里的帮助入口：一句话提示 + 指向 /help 的链接，取代原先的成段解释。 */
.bar-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-left: 10px;
  white-space: nowrap;
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
