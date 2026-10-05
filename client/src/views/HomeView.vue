<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useRoomStore } from '../stores/room'
import { rememberJoin } from '../utils/joinSession'
import type { CreateRoomResponse, Role, RoomInfoResponse } from '../types/protocol'

const router = useRouter()
const store = useRoomStore()

const NAME_KEY = 'pr:name'

const displayName = ref(localStorage.getItem(NAME_KEY) ?? `观众${Math.floor(Math.random() * 900 + 100)}`)
const createPassword = ref('')
const joinCode = ref('')
const joinPassword = ref('')
const busy = ref(false)
const error = ref('')

function remember(roomId: string, password: string, role: Role) {
  rememberJoin(roomId, { password, role, displayName: displayName.value })
}

async function createRoom() {
  error.value = ''
  const name = displayName.value.trim()
  if (!name) {
    error.value = '请先填写昵称'
    return
  }

  busy.value = true
  try {
    const resp = await fetch('/api/rooms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: createPassword.value }),
    })
    if (!resp.ok) {
      error.value = `创建房间失败（HTTP ${resp.status}）`
      return
    }

    const data = (await resp.json()) as CreateRoomResponse
    localStorage.setItem(NAME_KEY, name)
    store.setIceServers(data.iceServers ?? [])
    remember(data.roomId, createPassword.value, 'host')
    await router.push(`/room/${data.roomId}`)
  } catch (err) {
    error.value = `创建房间失败：${(err as Error).message}`
  } finally {
    busy.value = false
  }
}

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
        <div class="field">
          <label>你的昵称</label>
          <input v-model="displayName" maxlength="24" placeholder="主播昵称" />
        </div>
        <div class="field">
          <label>房间密码（可留空）</label>
          <input v-model="createPassword" type="password" placeholder="留空表示谁都能进" />
        </div>
        <button class="primary" :disabled="busy" :aria-busy="busy" @click="createRoom">
          {{ busy ? '创建中…' : '创建房间' }}
        </button>
        <p class="muted note">
          创建后会拿到 6 位房间码。进房后选好分片目录即可开播：分片由主播通过 WebRTC 直连分发，
          服务器不在视频链路上。本机没有 ffmpeg 时，主播页里有「服务端切片」与「一键切片脚本」两条路。
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
</style>
