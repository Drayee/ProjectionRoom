<script setup lang="ts">
/**
 * 发帖（/posts/new）。票 #15 定稿形态：Markdown 编辑/预览切换 + 封面 +
 * localStorage 草稿 + 退出确认（抄 YachiyoWeb 编辑体验）。
 * 草稿键沿用 pr:post-draft（逻辑契约，不属展示层）。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import MarkdownView from '../components/ui/MarkdownView.vue'
import * as postsApi from '../api/posts'
import { isCapabilityUnavailable, isMock } from '../api/mode'
import { usePostFailure, ensureLoggedInForWrite } from '../components/features/posts/usePostFailure'

const DRAFT_KEY = 'pr:post-draft'

const route = useRoute()
const router = useRouter()
const { failure, run } = usePostFailure(() => route.fullPath)

const title = ref('')
const content = ref('')
const cover = ref('')
const mode = ref<'edit' | 'preview'>('edit')
const busy = ref(false)
const saved = ref(false)
const error = ref('')
/** 该分区后端未上线：整个表单降级成空态（没有可提交的目标，留着表单只会让人白填一遍）。 */
const capabilityMissing = ref(false)

/**
 * 进入页面时探测一次"域在不在"（真实模式下）。
 *
 * 为什么要探测而不是等提交才发现：用户可能填三分钟才点发布——那时的降级
 * 等于告诉他"刚才白写了"。探测走 `listPosts`（该域必然存在的读路径，一条
 * 兜底分页），代价是真实模式下每次进页面多一个轻量 GET；mock 模式永远有
 * "后端"，一次都不打。401/403/429 都算"域在"——只有整域 404 才换空态。
 */
async function probeCapability() {
  if (isMock('posts')) return
  try {
    await postsApi.listPosts({ page: 1 })
  } catch (err) {
    if (isCapabilityUnavailable(err)) capabilityMissing.value = true
  }
}

onMounted(() => {
  void probeCapability()
  try {
    const raw = localStorage.getItem(DRAFT_KEY)
    if (raw) {
      const d = JSON.parse(raw) as { title?: string; content?: string; cover?: string }
      if (d.title) title.value = d.title
      if (d.content) content.value = d.content
      if (d.cover) cover.value = d.cover
    }
  } catch {
    localStorage.removeItem(DRAFT_KEY)
  }
})

onBeforeUnmount(() => {
  if (!saved.value) persistDraft()
})

onBeforeRouteLeave(() => {
  if (saved.value) return true
  if (!title.value.trim() && !content.value.trim() && !cover.value.trim()) return true
  return window.confirm('草稿还没发布，确定要离开吗？')
})

function persistDraft() {
  try {
    localStorage.setItem(DRAFT_KEY, JSON.stringify({ title: title.value, content: content.value, cover: cover.value }))
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
  // 发帖前先过本地登录闸门：省掉一次注定 401 的往返，也省掉"填完三分钟才发现要登录"。
  // 服务端那边仍有 RequireAuth 兜底（I3），这里只是体验前置。
  if (!ensureLoggedInForWrite()) {
    busy.value = false
    return
  }
  const made = await run(() =>
    postsApi.createPost({ title: t, content: c, images: cover.value.trim() ? [cover.value.trim()] : [] }),
  )
  busy.value = false
  if (made === null) {
    if (failure.value?.kind === 'capability') {
      // 后端该域未上线：表单已经没有意义，直接换成降级空态。
      capabilityMissing.value = true
    }
    // 其它失败由 failure 驱动的那行文案说明（401 已经跳了登录页）。
    return
  }
  clearDraft()
  saved.value = true
  await router.push({ name: 'posts' })
}

/** 失败态的一行文案（与 PostsPage / PostDetailPage 同一套分诊）。 */
const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能发帖'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

function discard() {
  clearDraft()
  void router.push({ name: 'posts' })
}
</script>

<template>
  <div class="page">
    <main class="body">
      <h1 class="title">发帖</h1>
      <CapabilityNotice
        v-if="capabilityMissing"
        domain="posts"
        hint="上线后这里支持标题 + 封面外链 + Markdown 正文，草稿仍会本地保存。"
      />
      <form v-else @submit.prevent="submit">
        <label for="post-title">标题</label>
        <input id="post-title" v-model="title" placeholder="一句话说清楚" />

        <label for="post-cover">封面（可选）</label>
        <input id="post-cover" v-model="cover" type="url" placeholder="封面图片 URL，留空则无封面" />

        <div class="bar">
          <label for="post-content">正文</label>
          <div class="seg" role="tablist" aria-label="编辑模式">
            <button type="button" role="tab" :aria-selected="mode === 'edit'" :class="{ on: mode === 'edit' }" @click="mode = 'edit'">编辑</button>
            <button type="button" role="tab" :aria-selected="mode === 'preview'" :class="{ on: mode === 'preview' }" @click="mode = 'preview'">预览</button>
          </div>
        </div>
        <textarea
          v-show="mode === 'edit'"
          id="post-content"
          v-model="content"
          rows="12"
          placeholder="支持 Markdown：**加粗**、# 标题、`代码`、> 引用、[链接](url)"
        />
        <MarkdownView v-if="mode === 'preview'" class="preview" :source="content" label="正文预览" />

        <p v-if="error" class="alert" role="alert">{{ error }}</p>
        <p v-else-if="failureText" class="alert" role="alert">{{ failureText }}</p>
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
.bar { display: flex; align-items: center; justify-content: space-between; margin-top: 10px; }
.bar label { margin: 0; }
.seg { display: inline-flex; border: 1px solid var(--line); }
.seg button { padding: 5px 14px; font-size: 13px; color: var(--dim); }
.seg button.on { color: var(--text); background: rgba(180, 108, 255, 0.14); }
.preview { border: 1px solid var(--line); padding: 12px 14px; min-height: 180px; background: rgba(0, 0, 0, 0.3); }
.alert { margin: 8px 0; padding: 8px 10px; border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.12); font-size: 12px; color: #ffdada; }
.row { display: flex; gap: 10px; margin-top: 14px; }
.primary { flex: 1; padding: 11px; background: var(--accent); color: var(--accent-ink); font-weight: 700; }
.primary:disabled { opacity: 0.5; }
.ghost { padding: 11px 18px; border: 1px solid var(--line); color: var(--dim); }
.ghost:hover { color: var(--text); border-color: var(--accent); }
.small { font-size: 12px; }
</style>
