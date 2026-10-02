<script setup lang="ts">
import { computed, ref } from 'vue'
import SegmentUpload from './SegmentUpload.vue'
import { useRoomStore } from '../stores/room'

const store = useRoomStore()
const seekTarget = ref('')
const dirInput = ref<HTMLInputElement | null>(null)
/** 服务端切片面板默认收起：不选它就不占地方，也不影响原有的"选择分片目录"流程。 */
const segmentOpen = ref(false)

const canControl = computed(() => store.isHost && store.joined)

const capacityText = computed(() => {
  const cap = store.capacity
  if (!cap) return '容量未知'
  const mbps = (cap.streamBps / 1_000_000).toFixed(2)
  if (cap.mode === 'pending') {
    return `码率 ${mbps} Mbps · 成员上限 ${cap.maxMembers} 人 · 等待实测上行`
  }
  const modeText = cap.mode === 'chain' ? '单链分发' : '扇出'
  return `码率 ${mbps} Mbps · ${modeText} K0=${cap.hostChildSlots} · 上限 ${cap.maxMembers} 人`
})

const mediaText = computed(() => {
  const index = store.mediaIndex
  if (!index) return ''
  return `${index.mimeType} · ${index.segments.length} 段 · ${(index.totalBytes / 1024 / 1024).toFixed(1)} MiB`
})

const uploadText = computed(() => {
  const bps = store.uploadCapacityBps
  if (!bps) return '未测得'
  return `${(bps / 1_000_000).toFixed(1)} Mbps`
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
  if (!Number.isFinite(seconds) || seconds < 0) return
  await store.seekTo(seconds)
  seekTarget.value = ''
}

function changeRate(event: Event) {
  const value = Number((event.target as HTMLSelectElement).value)
  if (Number.isFinite(value) && value > 0) store.setRate(value)
}
</script>

<template>
  <div class="host card">
    <header>
      <h2>主播控制台</h2>
      <span class="muted">{{ capacityText }}</span>
    </header>

    <div class="media">
      <input
        id="media-dir-input"
        ref="dirInput"
        class="hidden-input"
        type="file"
        webkitdirectory
        multiple
        @change="onDirectoryChosen"
      />
      <button :disabled="!store.joined" @click="pickDirectory">选择分片目录</button>
      <span v-if="mediaText" class="muted mono small">{{ mediaText }}</span>
      <span v-else class="muted small">未选片（先用 segmenter 预处理视频）</span>
    </div>

    <p class="error-text" v-if="store.mediaError">{{ store.mediaError }}</p>

    <!-- 本机没有 ffmpeg 的兜底入口：默认收起，展开后是上传 + 服务端切片面板。 -->
    <div class="segment-entry">
      <button class="segment-toggle" @click="segmentOpen = !segmentOpen">
        {{ segmentOpen ? '收起服务端切片' : '本机没有 ffmpeg？交给服务器切片' }}
      </button>
      <SegmentUpload v-if="segmentOpen" />
    </div>

    <div class="controls">
      <button v-if="store.playback.paused" class="primary" :disabled="!canControl || !store.mediaIndex" @click="store.play()">
        播放
      </button>
      <button v-else :disabled="!canControl" @click="store.pause()">暂停</button>

      <div class="seek">
        <input v-model="seekTarget" type="number" min="0" step="1" placeholder="秒" :disabled="!canControl" />
        <button :disabled="!canControl || seekTarget === ''" @click="seek">跳转</button>
      </div>

      <label class="rate">
        速率
        <select :value="store.playback.rate" :disabled="!canControl" @change="changeRate">
          <option :value="0.5">0.5x</option>
          <option :value="1">1.0x</option>
          <option :value="1.25">1.25x</option>
          <option :value="1.5">1.5x</option>
          <option :value="2">2.0x</option>
        </select>
      </label>
    </div>

    <div class="metrics mono small">
      <span class="badge mono">seq {{ store.playback.seq }}</span>
      <span>P2P {{ store.peerCount }} 连接</span>
      <span>上行 {{ uploadText }}</span>
      <span>已交付 {{ store.delivered }}</span>
      <span v-if="store.timedOut">超时 {{ store.timedOut }}</span>
    </div>
  </div>
</template>

<style scoped>
.host {
  padding: 12px;
}

header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}

h2 {
  margin: 0;
  font-size: 14px;
}

header .muted {
  font-size: 12px;
}

.hidden-input {
  display: none;
}

.media {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}

.small {
  font-size: 12px;
}

.error-text {
  margin: 0 0 10px;
  color: var(--danger);
  font-size: 12px;
  line-height: 1.6;
}

.segment-entry {
  margin-bottom: 10px;
}

button.segment-toggle {
  width: 100%;
  text-align: left;
  font-size: 12px;
  color: var(--text-dim);
  padding: 6px 10px;
}

.controls {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.seek {
  display: flex;
  gap: 6px;
  align-items: center;
}

.seek input {
  width: 92px;
}

.rate {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  color: var(--text-dim);
}

select {
  font: inherit;
  color: var(--text);
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px;
}

.metrics {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  align-items: center;
  margin-top: 10px;
  color: var(--text-dim);
}
</style>
