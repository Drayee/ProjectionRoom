<script setup lang="ts">
/**
 * 聊天面板（features/room）。看片优先布局右栏中部。
 * 只渲染；输入与滚底逻辑在 useChatPanel。
 */
import BrandIcon from '../../ui/BrandIcon.vue'
import { useChatPanel } from './useChatPanel'

const c = useChatPanel()
</script>

<template>
  <section class="chat">
    <h2>
      <BrandIcon name="chat" decorative :size="15" />
      聊天
      <span class="dim">{{ c.store.chat.length }}</span>
    </h2>
    <ol ref="c.listRef" class="list">
      <li v-for="m in c.store.chat" :key="m.key" :class="{ mine: m.mine }">
        <span class="who">{{ m.mine ? '我' : m.displayName }}</span>
        <span class="what">{{ m.text }}</span>
      </li>
      <li v-if="c.store.chat.length === 0" class="dim empty">还没有人说话。</li>
    </ol>
    <form class="input" @submit.prevent="c.submit()">
      <input v-model="c.draft.value" maxlength="500" placeholder="说点什么…" aria-label="聊天输入" />
      <button type="submit" :disabled="!c.draft.value.trim()" aria-label="发送">
        <BrandIcon name="msg" decorative :size="16" />
      </button>
    </form>
  </section>
</template>

<style scoped>
.chat {
  border: 1px solid var(--line);
  background: var(--surface);
  display: flex;
  flex-direction: column;
  min-height: 220px;
  flex: 1;
}

.chat h2 {
  margin: 0;
  padding: 10px 12px;
  border-bottom: 1px solid var(--line);
  font-size: 14px;
  display: flex;
  align-items: center;
  gap: 6px;
}

.list {
  flex: 1;
  margin: 0;
  padding: 8px 12px;
  list-style: none;
  overflow-y: auto;
  max-height: 34vh;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 13px;
}

.list li {
  display: flex;
  gap: 8px;
}

.who {
  flex: none;
  color: var(--dim);
}

.what {
  min-width: 0;
  overflow-wrap: anywhere;
}

.list li.mine .who {
  color: var(--accent);
}

.empty {
  text-align: center;
}

.input {
  display: flex;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--line);
}

.input input {
  flex: 1;
  min-width: 0;
}

.input button {
  flex: none;
  width: 36px;
  height: 36px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--line);
  color: var(--dim);
  transition: color var(--dur-micro) var(--ease), border-color var(--dur-micro) var(--ease);
}

.input button:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--accent);
}

.input button:disabled {
  opacity: 0.4;
}
</style>
