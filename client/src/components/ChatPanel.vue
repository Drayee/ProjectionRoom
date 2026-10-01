<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoomStore } from '../stores/room'

const store = useRoomStore()
const draft = ref('')
const listEl = ref<HTMLElement | null>(null)

const canSend = computed(() => store.joined && draft.value.trim().length > 0)

function submit() {
  if (!canSend.value) return
  store.sendChat(draft.value)
  draft.value = ''
}

watch(
  () => store.chat.length,
  async () => {
    await nextTick()
    if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight
  },
)

function formatTime(ts: number): string {
  const d = new Date(ts)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}
</script>

<template>
  <div class="chat card">
    <header>
      <h2>聊天</h2>
      <span class="muted">{{ store.chat.length }} 条</span>
    </header>

    <div class="list" ref="listEl">
      <p class="empty muted" v-if="store.chat.length === 0">还没有人说话</p>
      <div v-for="msg in store.chat" :key="msg.key" class="msg" :class="{ mine: msg.mine }">
        <div class="meta">
          <span class="who">{{ msg.displayName }}</span>
          <span class="muted">{{ formatTime(msg.ts) }}</span>
        </div>
        <div class="text">{{ msg.text }}</div>
      </div>
    </div>

    <form class="composer" @submit.prevent="submit">
      <input
        v-model="draft"
        maxlength="500"
        placeholder="说点什么…（回车发送）"
        :disabled="!store.joined"
      />
      <button class="primary" type="submit" :disabled="!canSend">发送</button>
    </form>
  </div>
</template>

<style scoped>
.chat {
  display: flex;
  flex-direction: column;
  min-height: 0;
  padding: 12px;
}

header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 8px;
}

h2 {
  margin: 0;
  font-size: 14px;
}

.list {
  flex: 1;
  min-height: 120px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding-right: 4px;
}

.empty {
  margin: 0;
  font-size: 12px;
}

.msg .meta {
  display: flex;
  gap: 8px;
  font-size: 12px;
}

.msg .who {
  color: var(--accent);
}

.msg.mine .who {
  color: var(--accent-2);
}

.msg .text {
  line-height: 1.5;
  word-break: break-word;
}

.composer {
  display: flex;
  gap: 8px;
  margin-top: 10px;
}

.composer button {
  white-space: nowrap;
}
</style>
