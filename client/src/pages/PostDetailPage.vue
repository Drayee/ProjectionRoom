<script setup lang="ts">
/**
 * 帖子详情（/posts/:id）。骨架阶段最小实现：正文纯文本插值。
 * 票 #15：#19 引入 Markdown 消毒出口（ui/MarkdownView.vue）后切换渲染。
 */
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import MarkdownView from '../components/ui/MarkdownView.vue'
import * as postsApi from '../api/posts'
import { isCapabilityUnavailable } from '../api/mode'
import type { Comment, Post } from '../types/posts'

const props = defineProps<{ id: string }>()
const router = useRouter()

const post = ref<Post | null>(null)
const comments = ref<Comment[]>([])
const draft = ref('')
const failed = ref('')
/** 该分区后端未上线：渲染降级空态，而不是「帖子不存在」。 */
const capabilityMissing = ref(false)

async function load() {
  capabilityMissing.value = false
  failed.value = ''
  try {
    post.value = await postsApi.getPost(Number(props.id))
    comments.value = await postsApi.listComments(Number(props.id))
  } catch (err) {
    if (isCapabilityUnavailable(err)) {
      capabilityMissing.value = true
    } else {
      failed.value = err instanceof Error ? err.message : '帖子不存在'
    }
  }
}

async function toggleLike() {
  if (!post.value) return
  try {
    const r = await postsApi.toggleLike(post.value.id)
    post.value.liked = r.liked
    post.value.likeCount = r.likeCount
  } catch (err) {
    if (isCapabilityUnavailable(err)) capabilityMissing.value = true
    else failed.value = err instanceof Error ? err.message : '操作失败'
  }
}

async function addComment() {
  const c = draft.value.trim()
  if (!c || !post.value) return
  try {
    const made = await postsApi.addComment({ postId: post.value.id, content: c })
    comments.value.push(made)
    post.value.commentCount += 1
    draft.value = ''
  } catch (err) {
    if (isCapabilityUnavailable(err)) capabilityMissing.value = true
    else failed.value = err instanceof Error ? err.message : '评论失败'
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <main class="body">
      <p v-if="failed" class="alert" role="alert">{{ failed }}</p>
      <CapabilityNotice
        v-else-if="capabilityMissing"
        domain="posts"
        hint="上线后这里是单篇帖子：正文、点赞与扁平评论。"
        retryable
        @retry="load"
      />
      <template v-else-if="post">
        <h1 class="title">{{ post.title }}</h1>
        <p class="meta dim">{{ post.author.displayName }} · {{ post.createdAt }}</p>
        <MarkdownView class="content" :source="post.content" label="帖子正文" />
        <div class="acts">
          <button :class="{ liked: post.liked }" type="button" @click="toggleLike">赞 {{ post.likeCount }}</button>
          <button type="button" @click="router.back()">返回</button>
        </div>
        <section class="comments">
          <h2>评论（{{ comments.length }}）</h2>
          <div v-for="c in comments" :key="c.id" class="cmt">
            <strong>{{ c.author.displayName }}</strong>
            <p>{{ c.content }}</p>
          </div>
          <form class="add" @submit.prevent="addComment">
            <input v-model="draft" placeholder="说点什么…" />
            <button type="submit" :disabled="!draft.trim()">发送</button>
          </form>
        </section>
      </template>
      <p v-else class="dim">加载中…</p>
    </main>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; }
.body { flex: 1; width: min(760px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 14px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(26px, 5vw, 38px); }
.meta { margin: 0; font-size: 12px; }
.content { margin: 0; }
.acts { display: flex; gap: 10px; }
.acts button { padding: 8px 18px; border: 1px solid var(--line); color: var(--dim); }
.acts button.liked { color: var(--glitch-p); border-color: var(--glitch-p); }
.acts button:hover { color: var(--text); border-color: var(--accent); }
.comments { border-top: 1px solid var(--line); padding-top: 16px; }
.comments h2 { margin: 0 0 10px; font-size: 15px; }
.cmt { padding: 8px 0; border-bottom: 1px dashed var(--line); font-size: 13px; }
.cmt p { margin: 4px 0 0; white-space: pre-wrap; }
.add { display: flex; gap: 8px; margin-top: 12px; }
.add button { flex: none; padding: 8px 18px; background: var(--accent); color: var(--accent-ink); font-weight: 700; }
.add button:disabled { opacity: 0.5; }
.alert { color: var(--danger); }
.alert[role='alert'] { margin: 0; padding: 8px 10px; border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.12); font-size: 12px; color: #ffdada; }
</style>
