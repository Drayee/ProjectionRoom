<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoomStore } from '../stores/room'

const store = useRoomStore()
const videoRef = ref<HTMLVideoElement | null>(null)

onMounted(() => {
  store.setVideoElement(videoRef.value)
})

onBeforeUnmount(() => {
  store.setVideoElement(null)
})

const state = computed(() => store.playback)

function formatTime(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds))
  const mm = String(Math.floor(total / 60)).padStart(2, '0')
  const ss = String(total % 60).padStart(2, '0')
  return `${mm}:${ss}`
}

const statusText = computed(() => {
  if (store.playerError) return store.playerError
  if (!store.mediaIndex) return store.isHost ? '等待选择分片目录' : '等待主播开播'
  if (store.gated) return `加载中 ${store.gateBufferedSec.toFixed(1)}s`
  if (store.needsGesture) return '需要一次点击才能播放'
  if (state.value.paused) return '已暂停'
  return '播放中'
})
</script>

<template>
  <div class="stage">
    <!-- 真实播放器：MediaSource 把所有分片按序拼进这里 -->
    <video ref="videoRef" class="video" playsinline :controls="!store.gated"></video>

    <div class="overlay" v-if="!store.mediaIndex">
      <p class="big">{{ store.isHost ? '选择分片目录后开播' : '等待主播开播' }}</p>
      <p class="muted">
        {{
          store.isHost
            ? '先用 cmd/segmenter 把视频切成 fMP4 分片目录，再在下方选择该目录。'
            : '主播还没选片。开播后这里会自动缓冲并跟随主播进度。'
        }}
      </p>
    </div>

    <!-- 启动门控：缓冲不到量就一直是"加载中"，且不设超时上限 -->
    <div class="overlay gate" v-else-if="store.gated">
      <p class="big">缓冲中 {{ store.gateBufferedSec.toFixed(1) }}s / {{ store.gateThresholdSec }}s</p>
      <p class="muted">{{ store.gateReason }} · 已等待 {{ store.gateWaitedSec.toFixed(0) }}s（加载不设超时）</p>
      <p class="muted hint" v-if="store.gateWaitedSec > 20">
        上游带宽可能不足。主播可换用低码率预设重新切片，或等上游把缓冲补齐后再开始。
      </p>
    </div>

    <div class="overlay gesture" v-else-if="store.needsGesture" @click="store.resumeAfterGesture()">
      <p class="big">点击继续播放</p>
      <p class="muted">浏览器自动播放策略要求一次用户手势才能播放声音</p>
    </div>

    <div class="status mono">
      <span :class="{ live: !state.paused && !store.needsGesture }">{{ statusText }}</span>
      <span>进度 {{ formatTime(state.currentTime) }}</span>
      <span v-if="!store.isHost" :class="{ warn: Math.abs(store.drift) > 0.5 }">
        偏差 {{ (store.drift * 1000).toFixed(0) }}ms
      </span>
      <span v-if="!store.isHost">缓冲 {{ store.bufferedAhead.toFixed(1) }}s</span>
      <span v-if="!store.isHost">矫正 {{ store.syncMode }}</span>
      <span>seq {{ state.seq }}</span>
    </div>
  </div>
</template>

<style scoped>
.stage {
  position: relative;
  flex: 1;
  min-height: 300px;
  background: #000;
  border: 1px solid var(--border);
  border-radius: 10px;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.video {
  width: 100%;
  height: 100%;
  flex: 1;
  background: #000;
  display: block;
}

.overlay {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: center;
  text-align: center;
  gap: 8px;
  padding: 20px;
  background: rgba(8, 8, 12, 0.78);
}

.overlay.gate .hint {
  color: var(--accent);
}

.overlay.gesture {
  cursor: pointer;
  background: rgba(8, 8, 12, 0.86);
}

.overlay .big {
  font-size: 18px;
  margin: 0;
}

.overlay .muted {
  max-width: 520px;
  line-height: 1.7;
  font-size: 13px;
}

.status {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--text-dim);
  padding: 6px 10px;
  background: rgba(8, 8, 12, 0.9);
  border-top: 1px solid var(--border);
}

.status .live {
  color: var(--ok);
}

.status .warn {
  color: var(--danger);
}
</style>
