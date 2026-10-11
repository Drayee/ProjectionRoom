<script setup lang="ts">
/**
 * 发帖页面（月夜玻璃，最小版）。
 * 标题 + 正文 textarea + 可选图片 + localStorage 草稿。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
import * as postsApi from '../api/posts'

const DRAFT_KEY = 'pr:post-draft'
const router = useRouter()
const title = ref('')
const content = ref('')
const images = ref<string[]>([])
const busy = ref(false)
const saved = ref(false)
const error = ref('')

onMounted(() => {
  try {
    const raw = localStorage.getItem(DRAFT_KEY)
    if (raw) {
      const d = JSON.parse(raw) as { title?: string; content?: string; images?: string[] }
      if (d.title) title.value = d.title
      if (d.content) content.value = d.content
      if (d.images) images.value = d.images
    }
  } catch { localStorage.removeItem(DRAFT_KEY) }
})

onBeforeUnmount(() => { if (!saved.value) persistDraft() })

function persistDraft() {
  try { localStorage.setItem(DRAFT_KEY, JSON.stringify({ title: title.value, content: content.value, images: images.value })) } catch {}
}
function clearDraft() { try { localStorage.removeItem(DRAFT_KEY) } catch {}; saved.value = false }

async function submit() {
  if (busy.value) return
  const t = title.value.trim()
  const c = content.value.trim()
  if (!t) { error.value = '请输入标题'; return }
  if (!c) { error.value = '请输入内容'; return }
  busy.value = true; error.value = ''
  try {
    await postsApi.createPost({ title: t, content: c, images: images.value })
    clearDraft(); saved.value = true
    await router.push({ name: 'posts' })
  } catch (err) {
    error.value = err instanceof Error ? err.message : '发布失败'
  } finally { busy.value = false }
}

function pickImage() { images.value.push(`https://picsum.photos/seed/${Date.now()}/400/300`) }
</script>

<template>
  <div class="page-glass">
    <AppHeader variant="shell" />

    <header class="head"><BrandIcon name="chat" decorative :size="20" /><h1>写帖子</h1></header>

    <div v-if="error" class="error-bar">
      <BrandIcon name="alert-triangle" label="错误" :size="15" />
      <span class="msg">{{ error }}</span>
      <button type="button" class="icon-btn" aria-label="关闭" @click="error = ''"><BrandIcon name="close" decorative :size="14" /></button>
    </div>

    <form class="form" @submit.prevent="submit" novalidate>
      <label class="label" for="post-title">标题</label>
      <input id="post-title" v-model="title" class="input" type="text" placeholder="给帖子起个标题" maxlength="100" />
      <span class="counter">{{ title.length }} / 100</span>

      <label class="label" for="post-content">正文</label>
      <textarea id="post-content" v-model="content" class="textarea" placeholder="写点什么…（纯文本，换行保留）" rows="8" maxlength="5000" />
      <span class="counter">{{ content.length }} / 5000</span>

      <div class="img-section">
        <button type="button" class="img-btn" @click="pickImage"><BrandIcon name="upload" decorative :size="15" /> 添加图片</button>
        <div v-if="images.length" class="img-list">
          <div v-for="(img, i) in images" :key="i" class="img-item">
            <img :src="img" alt="" class="img-thumb" />
            <button type="button" class="img-remove" @click="images.splice(i, 1)"><BrandIcon name="close" decorative :size="12" /></button>
          </div>
        </div>
      </div>

      <div class="actions">
        <RouterLink class="cancel" :to="{ name: 'posts' }">取消</RouterLink>
        <button type="submit" class="primary" :disabled="busy" :aria-busy="busy">{{ busy ? '发布中…' : '发布' }}</button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.head { display: flex; align-items: center; gap: 8px; }
.head h1 { margin: 0; font-size: 20px; }

.form { display: flex; flex-direction: column; gap: 10px; }
.label { font-size: 13px; font-weight: 600; }
.input, .textarea {
  font: inherit; color: var(--text); background: var(--panel-2);
  border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 10px;
  width: 100%;
}
.input:focus, .textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 2px rgba(0, 161, 214, 0.25); }
.textarea { resize: vertical; min-height: 160px; }
.counter { align-self: flex-end; font-size: 12px; color: var(--text-dim); }

.img-section { display: flex; flex-direction: column; gap: 8px; }
.img-btn {
  display: inline-flex; align-items: center; gap: 6px; padding: 8px 14px;
  background: transparent; border: 1px solid var(--border); border-radius: var(--radius-pill);
  color: var(--text-dim); font-size: 13px; cursor: pointer; align-self: flex-start;
  transition: var(--transition-fast);
}
.img-btn:hover { color: var(--text); border-color: var(--accent); }
.img-list { display: flex; gap: 8px; flex-wrap: wrap; }
.img-item { position: relative; }
.img-thumb { width: 80px; height: 80px; border-radius: var(--radius-sm); object-fit: cover; background: var(--panel-2); }
.img-remove {
  position: absolute; top: -6px; right: -6px;
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; padding: 0; border-radius: 50%;
  background: var(--danger); border: 1px solid var(--danger); color: #fff; cursor: pointer; line-height: 0;
}

.actions { display: flex; justify-content: flex-end; gap: 10px; padding-top: 10px; border-top: 1px solid var(--border); }
.cancel {
  padding: 8px 20px; border-radius: var(--radius-pill);
  background: transparent; border: 1px solid var(--border);
  color: var(--text-dim); text-decoration: none; font-size: 13px;
  transition: var(--transition-fast);
}
.cancel:hover { color: var(--text); border-color: var(--accent); }
button.primary {
  padding: 8px 24px; border-radius: var(--radius-pill);
  background: var(--accent); border: 1px solid var(--accent); color: var(--accent-ink);
  font-weight: 600; cursor: pointer;
}
button.primary:hover:not(:disabled) { filter: brightness(1.08); }
button.primary:disabled { opacity: 0.45; cursor: not-allowed; }
button.icon-btn { display: inline-flex; align-items: center; justify-content: center; padding: 3px; background: transparent; border-color: transparent; color: inherit; line-height: 0; }

.error-bar .msg { flex: 1; min-width: 0; }
@media (prefers-reduced-motion: reduce) { .img-btn, .img-remove, .cancel, button.primary { transition: none; } }
</style>
