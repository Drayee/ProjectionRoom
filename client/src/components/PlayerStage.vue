<script setup lang="ts">
import { computed } from 'vue'
import { useRoomStore } from '../stores/room'

const store = useRoomStore()
const state = computed(() => store.playback)

function formatTime(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds))
  const mm = String(Math.floor(total / 60)).padStart(2, '0')
  const ss = String(total % 60).padStart(2, '0')
  return `${mm}:${ss}`
}
</script>

<template>
  <div class="stage">
    <div class="placeholder">
      <p class="big">M2 接入视频链路</p>
      <p class="muted">
        主播用 ffmpeg 把本地视频预处理成 fMP4 分片目录后，这里会用 MediaSource 播放，
        并通过 WebRTC DataChannel 从主播/上级节点拉取分片。当前阶段先验证房间、同步信令与房主控制。
      </p>
    </div>
    <div class="status mono">
      <span :class="{ live: !state.paused }">{{ state.paused ? '已暂停' : '播放中' }}</span>
      <span>进度 {{ formatTime(state.currentTime) }}</span>
      <span>seq {{ state.seq }}</span>
      <span>速率 {{ state.rate.toFixed(2) }}x</span>
    </div>
  </div>
</template>

<style scoped>
.stage {
  flex: 1;
  min-height: 260px;
  background: #000;
  border: 1px solid var(--border);
  border-radius: 10px;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  padding: 20px;
}

.placeholder {
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: center;
  text-align: center;
  gap: 8px;
}

.placeholder .big {
  font-size: 18px;
  margin: 0;
}

.placeholder .muted {
  max-width: 520px;
  line-height: 1.7;
  font-size: 13px;
}

.status {
  display: flex;
  gap: 18px;
  font-size: 12px;
  color: var(--text-dim);
}

.status .live {
  color: var(--ok);
}
</style>
