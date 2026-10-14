<script setup lang="ts">
/**
 * 主播控制台（features/room）。看片优先布局的右栏上方（票 #17）。
 * 只渲染；元数据编辑逻辑在 useHostControls，选片/播放控制直调 store 动作。
 *
 * 选片：新 file input（**不**用旧的 #media-dir-input，票 #12 已退役该 DOM id），
 * 选后调 store.publishMediaDirectory(files) —— 这是普通用户的选片路径，
 * 不能拿调试钩子代替。
 */
import { computed, ref } from 'vue'
import BrandIcon from '../../ui/BrandIcon.vue'
import { useRoomStore } from '../../../stores/room'
import { useHostControls } from './useHostControls'

const store = useRoomStore()
const c = useHostControls()

const dirInput = ref<HTMLInputElement | null>(null)
const seekTarget = ref('')
const seeking = ref(false)
const starting = ref(false)

const canControl = computed(() => store.isHost && store.joined)

const mediaText = computed(() => {
  const index = store.mediaIndex
  if (!index) return ''
  return `${index.mimeType} · ${index.segments.length} 段 · ${(index.totalBytes / 1024 / 1024).toFixed(1)} MiB`
})

const stateText = computed(() => {
  if (store.mediaError) return '开播失败'
  if (store.mediaLoading) return '正在读取分片目录…'
  if (!store.mediaIndex) return '未选片'
  if (!store.videoReady) return '正在挂载播放器…'
  return store.playback.paused ? '已暂停' : '播放中'
})

const capacityText = computed(() => {
  const cap = store.capacity
  if (!cap) return '容量未知'
  const mbps = (cap.streamBps / 1_000_000).toFixed(2)
  if (cap.mode === 'pending') return `码率 ${mbps} Mbps · 上限 ${cap.maxMembers} 人 · 等待实测上行`
  const modeText = cap.mode === 'chain' ? '单链分发' : '扇出'
  return `码率 ${mbps} Mbps · ${modeText} · 上限 ${cap.maxMembers} 人`
})

function pickDirectory() {
  dirInput.value?.click()
}

async function onDirectoryChosen(event: Event) {
  const input = event.target as HTMLInputElement
  if (input.files && input.files.length > 0) {
    await store.publishMediaDirectory(input.files)
  }
  input.value = ''
}

async function seek() {
  const seconds = Number(seekTarget.value)
  if (!Number.isFinite(seconds) || seconds < 0 || seeking.value) return
  seeking.value = true
  try {
    await store.seekTo(seconds)
    seekTarget.value = ''
  } finally {
    seeking.value = false
  }
}

async function startPlay() {
  if (starting.value) return
  starting.value = true
  try {
    await store.play()
  } finally {
    starting.value = false
  }
}

function changeRate(event: Event) {
  const value = Number((event.target as HTMLSelectElement).value)
  if (Number.isFinite(value) && value > 0) store.setRate(value)
}
</script>

<template>
  <section class="host">
    <h2>
      <BrandIcon name="video" decorative :size="15" />
      主播控制
    </h2>

    <!-- 选片：唯一入口 -->
    <div class="block">
      <input
        ref="dirInput"
        type="file"
        webkitdirectory
        multiple
        class="hidden-input"
        aria-hidden="true"
        tabindex="-1"
        @change="onDirectoryChosen"
      />
      <button class="pick" type="button" @click="pickDirectory">
        <BrandIcon name="upload" decorative :size="15" />
        选择本地分片目录
      </button>
      <p class="dim small">先在本机把视频切成 fMP4 分片目录（见帮助「片源准备」），再选它。</p>
    </div>

    <p class="state">
      <span class="dot" :class="{ on: store.mediaIndex && store.videoReady }" aria-hidden="true" />
      {{ stateText }}
    </p>
    <p v-if="mediaText" class="dim small mono">{{ mediaText }}</p>
    <p v-if="store.mediaError" class="alert" role="alert">{{ store.mediaError }}</p>

    <div class="block">
      <div class="row">
        <button class="primary" type="button" :disabled="!canControl || starting" @click="startPlay">播放</button>
        <button class="ghost" type="button" :disabled="!canControl" @click="store.pause()">暂停</button>
      </div>
      <div class="row">
        <input v-model="seekTarget" type="number" min="0" step="1" placeholder="跳转秒数" aria-label="跳转秒数" />
        <button class="ghost" type="button" :disabled="!canControl || seeking || !seekTarget" @click="seek">跳转</button>
      </div>
      <div class="row">
        <label for="rate">速率</label>
        <select id="rate" :disabled="!canControl" :value="String(store.playback.rate)" @change="changeRate">
          <option value="0.5">0.5×</option>
          <option value="1">1×</option>
          <option value="1.25">1.25×</option>
          <option value="1.5">1.5×</option>
          <option value="2">2×</option>
        </select>
      </div>
      <p class="dim small">{{ capacityText }}</p>
    </div>

    <!-- 房间元数据（仅房主可见） -->
    <div v-if="c.showMeta.value" class="block meta">
      <label for="room-title">房间标题</label>
      <div class="row">
        <input id="room-title" v-model="c.titleDraft.value" maxlength="80" placeholder="可留空" @input="c.onTitleInput" />
        <button class="ghost" type="button" :disabled="c.metaBusy.value || c.tooLong.value" @click="c.saveTitle">保存</button>
      </div>
      <p v-if="c.tooLong.value" class="hint">标题超过 {{ c.meta.titleLimit }} 字上限。</p>
      <p v-if="c.meta.error" class="hint warn">{{ c.meta.error }}</p>
      <label class="check">
        <input type="checkbox" :checked="c.meta.isPublic" @change="c.onPublicToggle" />
        公开到「公开的房间」列表
      </label>
    </div>
  </section>
</template>

<style scoped>
.host {
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.host h2 {
  margin: 0 0 4px;
  font-size: 15px;
  display: flex;
  align-items: center;
  gap: 6px;
}

.block {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.block.meta {
  border-top: 1px solid var(--line);
  padding-top: 10px;
}

.row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.row label {
  margin: 0;
  flex: none;
}

.row input[type='number'] {
  flex: 1;
  min-width: 0;
}

.hidden-input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
  pointer-events: none;
}

.pick {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 10px;
  border: 1px solid var(--accent);
  color: var(--accent);
  font-weight: 600;
  transition: background var(--dur-micro) var(--ease), transform var(--dur-micro) var(--ease);
}

.pick:hover {
  background: rgba(180, 108, 255, 0.12);
}

.pick:active {
  transform: scale(0.98);
}

.state {
  margin: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--dim);
  flex: none;
}

.dot.on {
  background: var(--ok);
}

.small {
  font-size: 12px;
  margin: 0;
}

.primary {
  flex: 1;
  padding: 9px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
}

.primary:disabled {
  opacity: 0.45;
}

.ghost {
  padding: 9px 14px;
  border: 1px solid var(--line);
  color: var(--dim);
  transition: color var(--dur-micro) var(--ease), border-color var(--dur-micro) var(--ease);
}

.ghost:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--accent);
}

.ghost:disabled {
  opacity: 0.45;
}

select {
  flex: 1;
  min-width: 0;
}

.alert {
  margin: 0;
  padding: 6px 8px;
  border: 1px solid var(--danger);
  background: rgba(255, 92, 92, 0.12);
  font-size: 12px;
  color: #ffdada;
}

.hint {
  margin: 0;
  font-size: 12px;
}

.hint.warn {
  color: var(--glitch-p);
}

.check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text);
  margin: 4px 0 0;
}

.check input {
  width: auto;
  flex: none;
}
</style>
