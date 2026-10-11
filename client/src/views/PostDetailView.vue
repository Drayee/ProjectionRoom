<script setup lang="ts">
/**
 * 帖子详情页（月夜玻璃）。
 * 玻璃正文卡 + 评论扁平列表 + 互动乐观更新。
 */
import { useRouter } from 'vue-router'
import { onMounted, ref } from 'vue'
import AppHeader from '../components/AppHeader.vue'
import BrandIcon from '../components/BrandIcon.vue'
import * as postsApi from '../api/posts'
import type { Post, Comment } from '../types/posts'

const props = defineProps<{ id: string }>()
const router = useRouter()

const post = ref<Post | null>(null)
const comments = ref<Comment[]>([])
const newComment = ref('')
const busy = ref(false)
const error = ref('')
const loaded = ref(false)

onMounted(async () => {
  const pid = Number(props.id)
  if (!Number.isFinite(pid)) { error.value = '帖子 ID 不合法'; loaded.value = true; return }
  try {
    const [p, comm] = await Promise.all([postsApi.getPost(pid), postsApi.listComments(pid)])
    post.value = p; comments.value = comm
  } catch (err) {
    error.value = err instanceof Error ? err.message : '加载失败'
  } finally { loaded.value = true }
})

async function doLike() {
  if (!post.value) return
  try {
    const r = await postsApi.toggleLike(post.value.id)
    post.value.liked = r.liked; post.value.likeCount = r.likeCount
  } catch { error.value = '操作失败' }
}

async function submitComment() {
  if (!post.value || !newComment.value.trim()) return
  busy.value = true
  try {
    const c = await postsApi.addComment({ postId: post.value.id, content: newComment.value.trim() })
    comments.value.unshift(c); post.value.commentCount += 1; newComment.value = ''
  } catch (err) {
    error.value = err instanceof Error ? err.message : '发布失败'
  } finally { busy.value = false }
}

async function removeComment(cid: number) {
  try {
    await postsApi.deleteComment(cid)
    comments.value = comments.value.filter(c => c.id !== cid)
    if (post.value) post.value.commentCount -= 1
  } catch (err) {
    error.value = err instanceof Error ? err.message : '删除失败'
  }
}

function goBack() { void router.push({ name: 'posts' }) }
</script>

<template>
  <div class="page-glass">
    <AppHeader variant="shell" />

    <button type="button" class="back-btn" @click="goBack"><BrandIcon name="close" decorative :size="16" /> 返回</button>

    <div v-if="!loaded" class="muted center">加载中…</div>

    <div v-if="error && loaded" class="error-bar">
      <BrandIcon name="alert-triangle" label="错误" :size="15" />
      <span class="msg">{{ error }}</span>
    </div>

    <template v-if="post && loaded">
      <article class="post-card glass">
        <header class="post-head">
          <span class="avatar-circle" :style="{ background: post.author.avatarColor }">{{ post.author.avatarChar }}</span>
          <div class="post-info">
            <h1 class="post-title">{{ post.title }}</h1>
            <div class="post-meta"><span>{{ post.author.displayName }}</span> <span class="muted">· {{ new Date(post.createdAt).toLocaleString('zh-CN') }}</span></div>
          </div>
        </header>
        <div class="post-body">
          <p class="post-text">{{ post.content }}</p>
          <div v-if="post.images.length" class="post-imgs">
            <img v-for="(img, i) in post.images" :key="i" :src="img" alt="" class="post-img" decoding="async" />
          </div>
        </div>
        <div class="post-interact">
          <button type="button" class="interact-btn" :class="{ liked: post.liked }" @click="doLike">
            <BrandIcon name="star" decorative :size="15" /> {{ post.likeCount }}
          </button>
          <span class="interact-btn"><BrandIcon name="chat" decorative :size="15" /> {{ post.commentCount }}</span>
        </div>
      </article>

      <section class="comments">
        <h2 class="section-title">评论</h2>
        <div class="comment-form">
          <textarea v-model="newComment" class="comment-input" placeholder="写评论…" rows="3" maxlength="500" @keydown.ctrl.enter="submitComment" @keydown.meta.enter="submitComment" />
          <button type="button" class="submit-btn" :disabled="busy || !newComment.trim()" :aria-busy="busy" @click="submitComment">{{ busy ? '发布中…' : '发布' }}</button>
        </div>
        <div v-if="comments.length === 0" class="muted center">还没有评论</div>
        <div v-else class="comment-list">
          <div v-for="c in comments" :key="c.id" class="comment-item">
            <span class="avatar-sm" :style="{ background: c.author.avatarColor }">{{ c.author.avatarChar }}</span>
            <div class="comment-body">
              <div class="comment-head"><span class="comment-author">{{ c.author.displayName }}</span> <span class="comment-time">{{ new Date(c.createdAt).toLocaleString('zh-CN') }}</span></div>
              <p class="comment-text">{{ c.content }}</p>
              <button v-if="c.authorId === 1" type="button" class="comment-del" @click="removeComment(c.id)">删除</button>
            </div>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.back-btn {
  display: inline-flex; align-items: center; gap: 6px; padding: 6px 12px;
  background: transparent; border: 1px solid transparent; border-radius: var(--radius-sm);
  color: var(--text-dim); font-size: 13px; cursor: pointer; align-self: flex-start;
  transition: var(--transition-fast);
}
.back-btn:hover { color: var(--text); }
.center { text-align: center; padding: 40px 0; }

.post-card { display: flex; flex-direction: column; gap: 16px; padding: 20px; }
.post-head { display: flex; gap: 12px; }
.avatar-circle { display: inline-flex; align-items: center; justify-content: center; width: 40px; height: 40px; border-radius: 50%; color: #fff; font-weight: 700; font-size: 16px; flex: none; }
.post-info { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.post-title { margin: 0; font-size: 20px; line-height: 1.3; }
.post-meta { display: flex; align-items: center; gap: 4px; font-size: 13px; }
.post-body { display: flex; flex-direction: column; gap: 12px; }
.post-text { margin: 0; font-size: 14px; line-height: 1.7; white-space: pre-wrap; }
.post-imgs { display: flex; gap: 8px; flex-wrap: wrap; }
.post-img { max-width: 200px; max-height: 200px; border-radius: var(--radius-sm); object-fit: cover; background: var(--panel-2); }
.post-interact { display: flex; gap: 12px; }
.interact-btn {
  display: inline-flex; align-items: center; gap: 6px; padding: 6px 14px;
  border-radius: var(--radius-pill); border: 1px solid var(--border);
  background: transparent; color: var(--text-dim); font-size: 13px; cursor: pointer;
  transition: var(--transition-fast);
}
.interact-btn:hover { color: var(--text); border-color: var(--accent); }
.interact-btn.liked { color: var(--accent-2); border-color: var(--accent-2); }

.comments { display: flex; flex-direction: column; gap: 14px; }
.section-title { margin: 0; font-size: 16px; }
.comment-form { display: flex; flex-direction: column; gap: 8px; }
.comment-input {
  font: inherit; color: var(--text); background: var(--panel-2);
  border: 1px solid var(--border); border-radius: var(--radius-sm);
  padding: 10px; width: 100%; resize: vertical; min-height: 60px;
}
.comment-input:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 2px rgba(0, 161, 214, 0.25); }
.submit-btn {
  align-self: flex-end; padding: 8px 20px;
  border-radius: var(--radius-pill); background: var(--accent);
  border: 1px solid var(--accent); color: var(--accent-ink); font-weight: 600; cursor: pointer;
}
.submit-btn:hover:not(:disabled) { filter: brightness(1.08); }
.submit-btn:disabled { opacity: 0.45; cursor: not-allowed; }

.comment-list { display: flex; flex-direction: column; gap: 10px; }
.comment-item { display: flex; gap: 10px; padding: 10px; border-radius: var(--radius-sm); background: var(--panel); border: 1px solid var(--border); }
.avatar-sm { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; border-radius: 50%; color: #fff; font-weight: 700; font-size: 12px; flex: none; }
.comment-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.comment-head { display: flex; align-items: center; gap: 6px; }
.comment-author { font-weight: 600; font-size: 13px; }
.comment-time { font-size: 12px; color: var(--text-dim); }
.comment-text { margin: 0; font-size: 13px; line-height: 1.5; white-space: pre-wrap; }
.comment-del { align-self: flex-start; padding: 2px 8px; background: transparent; border: 1px solid transparent; color: var(--danger); font-size: 12px; cursor: pointer; border-radius: 4px; }
.comment-del:hover { background: rgba(255, 92, 92, 0.1); }

.error-bar .msg { flex: 1; min-width: 0; }
@media (prefers-reduced-motion: reduce) { .back-btn, .interact-btn, .submit-btn { transition: none; } }
</style>
