<script setup lang="ts">
/**
 * 进房卡片（观众入口）。
 *
 * **进房（观众）不需要登录**：这是产品决定，这里刻意不做任何账号拦截 ——
 * 落地页（未登录）与核心页（已登录）用的是**同一个组件**，逻辑一分不改地搬过来：
 * 先 `GET /api/rooms/:code` 判存在与主播在不在，再把凭据写进 sessionStorage 并跳转。
 */
import { ref } from 'vue'
import { useRouter, RouterLink } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import { rememberJoin } from '../utils/joinSession'
import type { Role, RoomInfoResponse } from '../types/protocol'

const props = withDefaults(
  defineProps<{
    name: string
    showName?: boolean
    /** 卡片标题。 */
    heading?: string
    /** 落地页模式：强调"免登录直接进入"，按钮文案也换一个。 */
    direct?: boolean
  }>(),
  { showName: true, heading: '加入房间', direct: false },
)
const emit = defineEmits<{ (e: 'update:name', value: string): void }>()

const router = useRouter()

const NAME_KEY = 'pr:name'

const joinCode = ref('')
const joinPassword = ref('')
const busy = ref(false)
const error = ref('')

function onNameInput(event: Event) {
  emit('update:name', (event.target as HTMLInputElement).value)
}

function remember(roomId: string, password: string, role: Role) {
  rememberJoin(roomId, { password, role, displayName: props.name })
}

async function joinRoom() {
  error.value = ''
  const code = joinCode.value.trim().toUpperCase()
  const name = props.name.trim()
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
  <section class="card join-card" :class="{ direct: props.direct }">
    <h2>
      <BrandIcon name="ticket" decorative :size="16" />
      {{ props.heading }}
      <span class="badge ok" v-if="props.direct">免登录</span>
    </h2>

    <p class="muted note" v-if="props.direct">输入房间码即可进房看片，不需要账号。</p>

    <div class="field" v-if="props.showName">
      <label for="join-name">你的昵称</label>
      <input
        id="join-name"
        :value="props.name"
        maxlength="24"
        placeholder="观众昵称"
        data-testid="join-name"
        @input="onNameInput"
      />
    </div>

    <div class="field">
      <label for="join-code">
        <BrandIcon name="ticket" decorative :size="12" />
        房间码
      </label>
      <input
        id="join-code"
        v-model="joinCode"
        class="mono"
        maxlength="6"
        placeholder="6 位房间码"
        autocapitalize="characters"
        data-testid="join-room-code"
      />
    </div>

    <div class="field">
      <label for="join-password">
        <BrandIcon name="lock" decorative :size="12" />
        房间密码（若主播设置了）
      </label>
      <input
        id="join-password"
        v-model="joinPassword"
        type="password"
        placeholder="无密码可留空"
        data-testid="join-room-password"
      />
    </div>

    <div class="error-bar" v-if="error">
      <BrandIcon name="alert-triangle" label="错误" :size="15" />
      <span class="msg">{{ error }}</span>
      <button type="button" class="icon-btn" aria-label="关闭错误提示" title="关闭错误提示" @click="error = ''">
        <BrandIcon name="close" decorative :size="14" />
      </button>
    </div>

    <button
      class="primary"
      :disabled="busy"
      :aria-busy="busy"
      data-testid="join-room-submit"
      @click="joinRoom"
    >
      {{ busy ? (props.direct ? '进入中…' : '加入中…') : props.direct ? '进入房间' : '加入房间' }}
    </button>

    <p class="muted note">
      房间码与密码分别是什么、复制房间码失败怎么办，见
      <RouterLink :to="{ name: 'help', hash: '#room-code' }" class="hint-link">
        <BrandIcon name="tips" decorative :size="13" />
        帮助
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

.join-card.direct .note:first-of-type {
  margin: 0 0 14px;
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
