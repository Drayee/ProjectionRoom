<script setup lang="ts">
/**
 * 播放舞台（features/room）。看片优先布局的左主区域。
 *
 * 硬契约（票 #12 §3.4，逐字保留，旧 PlayerStage.vue:9/13/121-123）：
 *   - `store.setVideoElement(el)` 挂载 / `setVideoElement(null)` 卸载：
 *     这是 `<video>` 与 store 的唯一绑定通道；漏了它 debug 快照的
 *     `player.attached=false`、`video.*` 全空而页面看着正常，极难归因。
 *   - 自动播放被拦时的语义按钮：Tab 可达、Enter/Space 激活
 *     `store.resumeAfterGesture()`，恢复后覆盖层消失。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import BrandIcon from '../../ui/BrandIcon.vue'
import { useRoomStore } from '../../../stores/room'

const store = useRoomStore()
const videoRef = ref<HTMLVideoElement | null>(null)

onMounted(() => {
  store.setVideoElement(videoRef.value)
})

onBeforeUnmount(() => {
  store.setVideoElement(null)
})

function formatTime(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds))
  const mm = String(Math.floor(total / 60)).padStart(2, '0')
  const ss = String(total % 60).padStart(2, '0')
  return `${mm}:${ss}`
}

/** 断线恢复三态优先于常规状态：它们是用户此刻唯一需要知道的事。 */
const statusText = computed(() => {
  if (store.playerError) return store.playerError
  if (!store.mediaIndex) return store.isHost ? '等待选择分片目录' : '等待主播开播'
  if (store.roomUnrecoverable) return '房间已失效'
  if (store.hostOffline) return '主播掉线，等待重连'
  if (store.resumeNotice) return '等待主播重建房间'
  if (store.gated) return `加载中 ${store.gateBufferedSec.toFixed(1)}s`
  if (store.needsGesture) return '需要一次点击才能播放'
  return store.playback.paused ? '已暂停' : '播放中'
})

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
    <video ref="videoRef" class="video" playsinline :controls="!store.gated" />

    <!-- 未开播 -->
    <div v-if="!store.mediaIndex" class="overlay">
      <p class="big">{{ store.isHost ? '选择分片目录后开播' : '等待主播开播' }}</p>
      <p class="dim">
        {{ store.isHost ? '在右侧「选择本地分片目录」里选好分片目录即可开播。' : '主播还没选片。开播后这里会自动缓冲并跟随主播进度。' }}
      </p>
      <p v-if="signalingText" class="dim hint">
        当前信令：{{ signalingText }}。重连成功后会自动进房，不需要刷新页面。
      </p>
    </div>

    <!-- 不可恢复：说清楚下一步，而不是停在"房间不存在"上 -->
    <div v-else-if="store.roomUnrecoverable" class="overlay offline" data-testid="stage-unrecoverable">
      <p class="big">房间已失效</p>
      <p class="dim">{{ store.roomUnrecoverable }}</p>
    </div>

    <div v-else-if="store.hostOffline" class="overlay offline" data-testid="stage-host-offline">
      <p class="big">{{ store.hostOfflineText }}</p>
      <p class="dim">主播与服务端的连接断了（网络抖动、刷新、服务端重启都可能）。房间、成员与播放状态会被服务端保留，主播回来会自动继续，不需要刷新页面。</p>
    </div>

    <div v-else-if="store.resumeNotice" class="overlay offline" data-testid="stage-room-resume">
      <p class="big">{{ store.resumeNotice }}</p>
      <p class="dim">正在重试加入同一个房间码；超过 60 秒主播仍未重建就会明确提示失效。</p>
    </div>

    <div v-else-if="store.gated" class="overlay gate">
      <p class="big">缓冲中 {{ store.gateBufferedSegments }}/{{ store.gateThresholdSegments }} 片</p>
      <p class="dim">
        {{ store.gateReason }} · 已缓冲 {{ store.gateBufferedSec.toFixed(1) }}s · 已等待 {{ store.gateWaitedSec.toFixed(0) }}s（加载不设超时）
      </p>
      <p v-if="store.gateWaitedSec > 20" class="dim hint">上游带宽可能不足。主播可换用低码率预设重新切片，或等上游把缓冲补齐后再开始。</p>
    </div>

    <!-- 自动播放门控恢复：语义按钮，键盘可达 -->
    <div
      v-else-if="store.needsGesture"
      class="overlay gesture"
      role="button"
      tabindex="0"
      aria-label="点击继续播放"
      @click="store.resumeAfterGesture()"
      @keydown.enter.prevent="store.resumeAfterGesture()"
      @keydown.space.prevent="store.resumeAfterGesture()"
    >
      <BrandIcon name="play" decorative :size="22" />
      <p class="big">点击继续播放</p>
      <p class="dim">浏览器自动播放策略要求一次用户手势才能播放声音（按 Enter 也可以）</p>
    </div>

    <div class="status mono">
      <span :class="{ live: !store.playback.paused && !store.needsGesture }">{{ statusText }}</span>
      <span>进度 {{ formatTime(store.playback.currentTime) }}</span>
      <span v-if="!store.isHost" :class="{ warn: Math.abs(store.drift) > 0.5 }">偏差 {{ (store.drift * 1000).toFixed(0) }}ms</span>
      <span v-if="!store.isHost">缓冲 {{ store.bufferedAhead.toFixed(1) }}s</span>
      <span v-if="!store.isHost">矫正 {{ store.syncMode }}</span>
      <span v-if="lagText" :class="{ warn: store.lagSec > 2 }">{{ lagText }}</span>
      <span v-if="signalingText" class="warn">{{ signalingText }}</span>
      <span v-if="store.lagNotice" class="warn">{{ store.lagNotice }}</span>
      <span>seq {{ store.playback.seq }}</span>
    </div>
  </div>
</template>

<style scoped>
.stage {
  position: relative;
  flex: 1;
  min-height: 0;
  border: 1px solid var(--line);
  background: #05030a;
}

.video {
  display: block;
  width: 100%;
  height: 100%;
  max-height: calc(100vh - 130px);
  background: #000;
}

.overlay {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 24px;
  text-align: center;
  background: rgba(5, 3, 10, 0.82);
}

.overlay.offline {
  background: rgba(60, 10, 20, 0.85);
}

.overlay.gesture {
  cursor: pointer;
  color: var(--accent);
}

.overlay.gesture:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -4px;
}

.big {
  margin: 0;
  font-family: var(--font-glitch);
  font-size: 20px;
  letter-spacing: 0.04em;
}

.overlay p {
  margin: 0;
  max-width: 46ch;
  font-size: 13px;
}

.hint {
  font-size: 12px;
  opacity: 0.8;
}

.status {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  padding: 6px 10px;
  border-top: 1px solid var(--line);
  background: rgba(5, 3, 10, 0.9);
  font-size: 11px;
  color: var(--dim);
}

.status .live {
  color: var(--ok);
}

.status .warn {
  color: var(--glitch-p);
}

@media (max-width: 760px) {
  .video {
    max-height: 46vh;
  }
}
</style>
