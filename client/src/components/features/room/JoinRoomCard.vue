<script setup lang="ts">
/**
 * 进房卡片（观众入口，落地页形态）。
 * 逻辑在 useJoinRoom；本组件只渲染。归 features/room/：发请求。
 */
import { useRouter } from 'vue-router'
import { useJoinRoom } from './useJoinRoom'

const props = defineProps<{ name: () => string }>()

const router = useRouter()
const j = useJoinRoom(props.name)

function back(): void {
  void router.push({ name: 'public-rooms' })
}
</script>

<template>
  <form class="join" @submit.prevent="j.join()">
    <label for="join-code">房间码</label>
    <input
      id="join-code"
      v-model="j.code"
      maxlength="12"
      placeholder="4–12 位字母数字"
      autocapitalize="characters"
    />
    <label for="join-pwd">房间密码（没有就留空）</label>
    <input id="join-pwd" v-model="j.password" type="password" autocomplete="off" />
    <p v-if="j.error.value" class="alert" role="alert">{{ j.error.value }}</p>
    <div class="row">
      <button class="primary" type="submit" :disabled="j.busy.value">进入放映室</button>
      <button class="ghost" type="button" @click="back">看公开房间</button>
    </div>
  </form>
</template>

<style scoped>
.join {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.row {
  display: flex;
  gap: 10px;
  margin-top: 12px;
}

.primary {
  flex: 1;
  padding: 11px;
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
  transition: background var(--dur-micro) var(--ease), transform var(--dur-micro) var(--ease);
}

.primary:hover:not(:disabled) {
  background: var(--accent-hover);
}

.primary:active:not(:disabled) {
  transform: scale(0.98);
}

.primary:disabled {
  opacity: 0.5;
}

.ghost {
  padding: 11px 14px;
  border: 1px solid var(--line);
  color: var(--dim);
  transition: color var(--dur-micro) var(--ease), border-color var(--dur-micro) var(--ease);
}

.ghost:hover {
  color: var(--text);
  border-color: var(--accent);
}

.alert {
  margin: 6px 0 0;
  padding: 8px 10px;
  border: 1px solid var(--danger);
  background: rgba(255, 92, 92, 0.12);
  font-size: 12px;
  color: #ffdada;
}
</style>
