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

/** 落后主播的秒数只在不落后时隐藏；正数=落后，负数=领先。 */
const lagText = computed(() => {
  if (store.isHost) return ''
  const sec = store.lagSec
  if (Math.abs(sec) < 1) return ''
  return sec > 0 ? `落后 ${sec.toFixed(1)}s` : `领先 ${Math.abs(sec).toFixed(1)}s`
})

const signalingText = computed(() => {
  switch (store.connection) {
    case 'closed':
      return '信令断开，重连中'
    case 'connecting':
      return '信令连接中'
    case 'open':
      return store.joined ? '' : '正在进房'
    default:
      return '未连接'
  }
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
      <p class="muted hint" v-if="store.connection !== 'open'">
        当前与服务端的信令未就绪：{{ signalingText }}。重连成功后会自动进房，不需要刷新页面。
      </p>
      <p class="muted hint" v-else-if="!store.joined">正在加入房间…</p>
    </div>

    <!-- 启动门控：从主播时间戳所在分片起攒够连续 n 片才起播，且不设超时上限 -->
    <div class="overlay gate" v-else-if="store.gated">
      <p class="big">缓冲中 {{ store.gateBufferedSegments }}/{{ store.gateThresholdSegments }} 片</p>
      <p class="muted">
        {{ store.gateReason }} · 已缓冲 {{ store.gateBufferedSec.toFixed(1) }}s · 已等待
        {{ store.gateWaitedSec.toFixed(0) }}s（加载不设超时）
      </p>
      <p class="muted hint" v-if="store.gateWaitedSec > 20">
        上游带宽可能不足。主播可换用低码率预设重新切片，或等上游把缓冲补齐后再开始。
      </p>
    </div>

    <div
      class="overlay gesture"
      v-else-if="store.needsGesture"
      role="button"
      tabindex="0"
      @click="store.resumeAfterGesture()"
      @keydown.enter.prevent="store.resumeAfterGesture()"
      @keydown.space.prevent="store.resumeAfterGesture()"
    >
      <p class="big">点击继续播放</p>
      <p class="muted">浏览器自动播放策略要求一次用户手势才能播放声音（按 Enter 也可以）</p>
    </div>

    <div class="status mono">
      <span :class="{ live: !state.paused && !store.needsGesture }">{{ statusText }}</span>
      <span>进度 {{ formatTime(state.currentTime) }}</span>
      <span v-if="!store.isHost" :class="{ warn: Math.abs(store.drift) > 0.5 }">
        偏差 {{ (store.drift * 1000).toFixed(0) }}ms
      </span>
      <span v-if="!store.isHost">缓冲 {{ store.bufferedAhead.toFixed(1) }}s</span>
      <span v-if="!store.isHost">矫正 {{ store.syncMode }}</span>
      <!-- 落后主播：数据来自 store.lagSec（与验收快照同一份口径） -->
      <span v-if="lagText" :class="{ warn: store.lagSec > 2 }">{{ lagText }}</span>
      <span v-if="signalingText" class="warn">{{ signalingText }}</span>
      <!-- 滞后过久：已提示用户并跳转到主播当前进度（SPEC §7.5 卡顿策略） -->
      <span v-if="store.lagNotice" class="warn">{{ store.lagNotice }}</span>
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
