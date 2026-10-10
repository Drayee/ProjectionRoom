<script setup lang="ts">
/**
 * 建房卡片（主播入口）。
 *
 * 逻辑与拆分前逐字一致：**未登录就地拦截**（`ensureLoggedIn()` 带 `?redirect=` 去登录页，
 * 登录后回到本页），登录态失效（401）再拦一次，其余失败按 HTTP 状态给文案。
 * 昵称与创建密码都留在这里 —— 它们只有建房动作需要。
 */
import { ref } from 'vue'
import { useRouter, RouterLink } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { useAuthStore } from '../stores/auth'
import { useRoomStore } from '../stores/room'
import { rememberJoin } from '../utils/joinSession'
import type { CreateRoomResponse, Role } from '../types/protocol'

const props = withDefaults(defineProps<{ name: string; showName?: boolean }>(), { showName: true })
const emit = defineEmits<{ (e: 'update:name', value: string): void }>()

const router = useRouter()
const store = useRoomStore()
const auth = useAuthStore()

/** 本机昵称的存储键：与「加入房间」共用同一个值（`v-model:name` 指向 HomeView 的那一份）。 */
const NAME_KEY = 'pr:name'

const createPassword = ref('')
const busy = ref(false)
const error = ref('')

function onNameInput(event: Event) {
  emit('update:name', (event.target as HTMLInputElement).value)
}

function remember(roomId: string, password: string, role: Role, hostToken?: string) {
  rememberJoin(roomId, { password, role, displayName: props.name, hostToken })
}

async function createRoom() {
  error.value = ''
  // 建房必须登录（判据②）：**就地拦截**，未登录直接把人送去登录页并带上回跳路径，
  // 登录成功后回到这里（URL 与页面都还在），而不是先发一个注定 401 的请求。
  if (!auth.ensureLoggedIn()) {
    return
  }

  const name = props.name.trim()
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

/**
 * 把建房动作暴露给首页（FAB 的「新建房间」）。
 *
 * 为什么是"暴露同一个函数"而不是在 FAB 里再写一遍建房：建房这条路径上有未登录拦截
 * （`ensureLoggedIn` 带 `?redirect=`）、401 刷新重试、昵称空值、房间凭据落 sessionStorage、
 * 主播复位令牌随行 —— 抄一份出来必然会分叉。FAB 只负责"把卡片带到眼前 + 触发它"。
 */
defineExpose({ createRoom })
</script>

<template>
  <section class="card create-card">
    <h2>
      <BrandIcon name="video" decorative :size="16" />
      作为主播创建房间
    </h2>

    <!-- 未登录时先说明门槛，用户不必点一下才知道要登录（点击本身也会被就地拦截并送去登录页）。 -->
    <p class="muted note auth-hint" v-if="!auth.isLoggedIn" data-testid="home-create-login-hint">
      创建房间需要登录；<strong>只是进房看片不需要账号</strong>。
      <RouterLink :to="{ name: 'help', hash: '#why-login' }" class="hint-link">
        <BrandIcon name="tips" decorative :size="13" />
        为什么？
      </RouterLink>
    </p>

    <div class="field" v-if="showName">
      <label for="create-name">你的昵称</label>
      <input
        id="create-name"
        :value="props.name"
        maxlength="24"
        placeholder="主播昵称"
        @input="onNameInput"
      />
    </div>
    <div class="field">
      <label for="create-password">
        <BrandIcon name="lock" decorative :size="12" />
        房间密码（可留空）
      </label>
      <input id="create-password" v-model="createPassword" type="password" placeholder="留空表示谁都能进" />
    </div>

    <div class="error-bar" v-if="error">
      <BrandIcon name="alert-triangle" label="错误" :size="15" />
      <span class="msg">{{ error }}</span>
      <button type="button" class="icon-btn" aria-label="关闭错误提示" v-tip="'关闭错误提示'" @click="error = ''">
        <BrandIcon name="close" decorative :size="14" />
      </button>
    </div>

    <button class="primary" :disabled="busy" :aria-busy="busy" data-testid="home-create-room" @click="createRoom">
      {{ busy ? '创建中…' : '创建房间' }}
    </button>

    <p class="muted note">
      创建后会拿到 6 位房间码。片源怎么准备（本机切片器 / 服务端切片 / 下载切片器）见
      <RouterLink :to="{ name: 'help', hash: '#source' }" class="hint-link">
        <BrandIcon name="video" decorative :size="13" />
        片源准备
      </RouterLink>
      。
    </p>
  </section>
</template>

<style scoped>
h2 {
  margin: 0 0 12px;
  font-size: 16px;
  display: flex;
  align-items: center;
  gap: 7px;
}

.field {
  margin-bottom: 14px;
}

label {
  display: flex;
  align-items: center;
  gap: 5px;
}

button.primary {
  width: 100%;
}

.note {
  margin: 12px 0 0;
  font-size: 12px;
  line-height: 1.6;
}

/* 建房需要登录的说明：放在按钮**上方**，所以需要下边距（.note 默认是 0，用在卡片尾部）。 */
.auth-hint {
  margin: 0 0 14px;
  line-height: 1.6;
}

.hint-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--accent);
}

.error-bar {
  margin-bottom: 12px;
}

.error-bar .msg {
  flex: 1;
  min-width: 0;
}

button.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 3px;
  background: transparent;
  border-color: transparent;
  color: inherit;
}
</style>
