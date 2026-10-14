<script setup lang="ts">
/**
 * 发帖（/posts/new）。骨架阶段最小实现：标题 + 正文 + localStorage 草稿。
 * 票 #15：#19 升级为 Markdown 编辑/预览切换 + 封面（抄 YachiyoWeb 形态）。
 * 草稿键沿用 pr:post-draft（逻辑契约，不属展示层）。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppHeader from '../components/ui/AppHeader.vue'
import * as postsApi from '../api/posts'

const DRAFT_KEY = 'pr:post-draft'

const router = useRouter()
const route = useRoute()

const title = ref('')
const content = ref('')
const busy = ref(false)
const saved = ref(false)
const error = ref('')

onMounted(() => {
  try {
    const raw = localStorage.getItem(DRAFT_KEY)
    if (raw) {
      const d = JSON.parse(raw) as { title?: string; content?: string }
      if (d.title) title.value = d.title
      if (d.content) content.value = d.content
    }
  } catch {
    localStorage.removeItem(DRAFT_KEY)
  }
})

onBeforeUnmount(() => {
  if (!saved.value) persistDraft()
})

function persistDraft() {
  try {
    localStorage.setItem(DRAFT_KEY, JSON.stringify({ title: title.value, content: content.value }))
  } catch {
    /* 存储满等场景静默：草稿是便利，不是数据保证 */
  }
}

function clearDraft() {
  try {
    localStorage.removeItem(DRAFT_KEY)
  } catch {
    /* 同上 */
  }
  saved.value = false
}

async function submit() {
  if (busy.value) return
  const t = title.value.trim()
  const c = content.value.trim()
  if (!t) {
    error.value = '请输入标题'
    return
  }
  if (!c) {
    error.value = '请输入内容'
    return
  }
  busy.value = true
  error.value = ''
  try {
    await postsApi.createPost({ title: t, content: c })
    clearDraft()
    saved.value = true
    await router.push({ name: 'posts' })
  } catch (err) {
    error.value = err instanceof Error ? err.message : '发布失败'
  } finally {
    busy.value = false
  }
}

function discard() {
  clearDraft()
  void router.push({ name: 'posts' })
}

// 从别处带过来的初始标题（预留；当前 route.query 无消费方）
void route
</script>

<template>
  <div class="page">
    <AppHeader variant="page" />
    <main class="body">
      <h1 class="title">发帖</h1>
      <form @submit.prevent="submit">
        <label for="post-title">标题</label>
        <input id="post-title" v-model="title" placeholder="一句话说清楚" />
        <label for="post-content">正文</label>
        <textarea id="post-content" v-model="content" rows="12" placeholder="支持 Markdown（渲染即将上线）" />
        <p v-if="error" class="alert" role="alert">{{ error }}</p>
        <div class="row">
          <button class="primary" type="submit" :disabled="busy">发布</button>
          <button class="ghost" type="button" @click="discard">丢弃草稿</button>
        </div>
        <p class="dim small">离开页面会自动存草稿。</p>
      </form>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(680px, 100%); margin: 0 auto; padding: 32px 20px 64px; }
.title { margin: 0 0 16px; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
form { display: flex; flex-direction: column; gap: 6px; }
label { margin-top: 10px; }
textarea { resize: vertical; }
.alert { margin: 8px 0; padding: 8px 10px; border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.12); font-size: 12px; color: #ffdada; }
.row { display: flex; gap: 10px; margin-top: 14px; }
.primary { flex: 1; padding: 11px; background: var(--accent); color: var(--accent-ink); font-weight: 700; }
.primary:disabled { opacity: 0.5; }
.ghost { padding: 11px 18px; border: 1px solid var(--line); color: var(--dim); }
.ghost:hover { color: var(--text); border-color: var(--accent); }
.small { font-size: 12px; }
</style>
