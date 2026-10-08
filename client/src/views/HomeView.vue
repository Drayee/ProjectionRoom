<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import { useAuthStore } from '../stores/auth'
import { useRoomStore } from '../stores/room'
import { rememberJoin } from '../utils/joinSession'
import type { CreateRoomResponse, Role, RoomInfoResponse } from '../types/protocol'

const router = useRouter()
const store = useRoomStore()
const auth = useAuthStore()

const NAME_KEY = 'pr:name'

/**
 * 本机昵称。优先级：本机上次用过的 > **账号昵称**（登录了就不用再想办法起名字）> 随机。
 * 从登录页回跳到首页时组件会重新创建，所以用账号昵称兜底这件事在"登录后回到发起处"时也成立。
 */
const displayName = ref(
  localStorage.getItem(NAME_KEY) ?? auth.profile?.displayName ?? `观众${Math.floor(Math.random() * 900 + 100)}`,
)
const createPassword = ref('')
const joinCode = ref('')
const joinPassword = ref('')
const busy = ref(false)
const error = ref('')

function remember(roomId: string, password: string, role: Role, hostToken?: string) {
  rememberJoin(roomId, { password, role, displayName: displayName.value, hostToken })
}

async function createRoom() {
  error.value = ''
  // 建房必须登录（判据②）：**就地拦截**，未登录直接把人送去登录页并带上回跳路径，
  // 登录成功后回到这里（URL 与页面都还在），而不是先发一个注定 401 的请求。
  if (!auth.ensureLoggedIn()) {
    return
  }

  const name = displayName.value.trim()
  if (!name) {
    error.value = '请先填写昵称'
    return
  }

  busy.value = true
  try {
    // 带 access token（服务端 T8 起 POST /api/rooms 挂 RequireAuth）。
    // 401 时 authedFetch 会自动刷新一次再重试；刷新也失败才会抛错并把人送去登录页。
    const resp = await auth.authedFetch('/api/rooms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: createPassword.value }),
    })
    if (resp.status === 401) {
      // 走到这里说明"刷新一次再重试"之后仍然 401：不再重试，说清原因并把人送去登录页。
      error.value = '登录状态已失效，请重新登录后重试'
      void auth.redirectToLogin()
      return
    }
    if (!resp.ok) {
      error.value = `创建房间失败（HTTP ${resp.status}）`
      return
    }

    const data = (await resp.json()) as CreateRoomResponse
    localStorage.setItem(NAME_KEY, name)
    // 响应里除了 iceServers 还有 ttlSeconds/expiresAt/probe：一起存下来，
    // 进房后的 /api/ice 才能正确判断"列表到底变没变"（变了才 setConfiguration + ICE restart）。
    store.applyIceResponse(data, 'POST /api/rooms')
    // 主播复位令牌（S-7）只在这一次响应里出现（服务端只存哈希）：
    // 必须跟 join 凭据一起存下来，否则主播断线后**宽限期内接不回主播位**。
    remember(data.roomId, createPassword.value, 'host', data.hostToken)
    await router.push(`/room/${data.roomId}`)
  } catch (err) {
    error.value = `创建房间失败：${(err as Error).message}`
  } finally {
    busy.value = false
  }
}

/** 进房（观众）**不需要登录**：这是产品决定，这里刻意不做任何账号拦截。 */
async function joinRoom() {
  error.value = ''
  const code = joinCode.value.trim().toUpperCase()
  const name = displayName.value.trim()
  if (!code) {
    error.value = '请填写房间码'
    return
  }
  if (!name) {
    error.value = '请先填写昵称'
    return
  }

  busy.value = true
  try {
    const resp = await fetch(`/api/rooms/${encodeURIComponent(code)}`)
    const info = resp.ok ? ((await resp.json()) as RoomInfoResponse) : null

    if (!info?.exists) {
      error.value = `房间 ${code} 不存在`
      return
    }
    if (!info.hasHost) {
      error.value = `房间 ${code} 还没有主播进房，请稍候`
      return
    }

    localStorage.setItem(NAME_KEY, name)
    remember(code, joinPassword.value, 'viewer')
    await router.push(`/room/${code}`)
  } catch (err) {
    error.value = `加入房间失败：${(err as Error).message}`
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="home">
    <AppHeader variant="page" />

    <header class="home-head">
      <h1>ProjectionRoom</h1>
      <p class="muted">
        主播播放本地视频，观众通过 WebRTC 直连同步观看。服务器只做信令、房间状态与拓扑管理，不传输视频字节。
      </p>
    </header>

    <div class="error-bar" v-if="error">
      <span>{{ error }}</span>
      <button @click="error = ''">关闭</button>
    </div>

    <div class="grid">
      <section class="card">
        <h2>作为主播创建房间</h2>
        <!-- 未登录时先说明门槛，用户不必点一下才知道要登录（点击本身也会被就地拦截并送去登录页）。 -->
        <p class="muted note auth-hint" v-if="!auth.isLoggedIn" data-testid="home-create-login-hint">
          创建房间需要登录（点下面的按钮会跳转登录，登录后回到本页）；
          <strong>只是进房看片不需要账号</strong>，右侧输入房间码即可。
        </p>
        <div class="field">
          <label>你的昵称</label>
          <input v-model="displayName" maxlength="24" placeholder="主播昵称" />
        </div>
        <div class="field">
          <label>房间密码（可留空）</label>
          <input v-model="createPassword" type="password" placeholder="留空表示谁都能进" />
        </div>
        <button
          class="primary"
          :disabled="busy"
          :aria-busy="busy"
          data-testid="home-create-room"
          @click="createRoom"
        >
          {{ busy ? '创建中…' : '创建房间' }}
        </button>
        <p class="muted note">
          创建后会拿到 6 位房间码。进房后选好分片目录即可开播：分片由主播通过 WebRTC 直连分发，
          服务器不在视频链路上。本机没有 ffmpeg 时，主播页里有「服务端切片」与「下载切片工具」两条路。
        </p>
        <p class="muted note">
          片源要自备：本项目不提供也不分发任何内容。还没有片源的话，可以到第三方资源站
          <a
            href="https://www.comicat.org/"
            target="_blank"
            rel="noopener noreferrer"
            data-testid="anime-source-link"
            >comicat.org（番剧资源，新窗口打开）</a
          >。
        </p>
      </section>

      <section class="card">
        <h2>加入房间</h2>
        <div class="field">
          <label>房间码</label>
          <input v-model="joinCode" class="mono" maxlength="6" placeholder="6 位房间码" />
        </div>
        <div class="field">
          <label>房间密码（若主播设置了）</label>
          <input v-model="joinPassword" type="password" placeholder="无密码可留空" />
        </div>
        <button class="primary" :disabled="busy" :aria-busy="busy" @click="joinRoom">
          {{ busy ? '加入中…' : '加入房间' }}
        </button>
      </section>
    </div>
  </div>
</template>

<style scoped>
.home {
  max-width: 980px;
  margin: 0 auto;
  padding: 40px 20px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.home-head h1 {
  margin: 0 0 8px;
  font-size: 28px;
}

.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
}

@media (max-width: 760px) {
  .grid {
    grid-template-columns: 1fr;
  }
}

h2 {
  margin-top: 0;
  font-size: 16px;
}

.field {
  margin-bottom: 14px;
}

button {
  width: 100%;
}

.note {
  margin-bottom: 0;
  font-size: 12px;
  line-height: 1.6;
}

/* 建房需要登录的说明：放在按钮**上方**，所以需要下边距（.note 默认是 0，它用在卡片尾部）。 */
.auth-hint {
  margin: 0 0 14px;
  line-height: 1.6;
}
</style>
