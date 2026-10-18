<script setup lang="ts">
/**
 * 帖子详情（/posts/:id）。#26 切真：写操作按 401/403/429 分诊，点赞做乐观更新 +
 * 失败回滚，评论可删（仅作者，服务端判）。
 */
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import BrandIcon from '../components/ui/BrandIcon.vue'
import CapabilityNotice from '../components/ui/CapabilityNotice.vue'
import MarkdownView from '../components/ui/MarkdownView.vue'
import * as postsApi from '../api/posts'
import { isCapabilityUnavailable } from '../api/mode'
import { usePostFailure, ensureLoggedInForWrite } from '../components/features/posts/usePostFailure'
import { useAuthStore } from '../stores/auth'
import type { Comment, Post } from '../types/posts'

const props = defineProps<{ id: string }>()
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { failure, run } = usePostFailure(() => route.fullPath)

const post = ref<Post | null>(null)
const comments = ref<Comment[]>([])
const draft = ref('')
/** 该分区后端未上线：渲染降级空态，而不是「帖子不存在」。 */
const capabilityMissing = ref(false)
/** 正在取消点赞的那一条（用于把按钮按下去的视觉反馈）。 */
const likingBusy = ref(false)

/** 这条评论是不是当前登录用户发的（决定显不显示删除按钮）。 */
function isMine(c: Comment): boolean {
  return auth.profile?.id === c.authorId
}

/** 失败态 → 一行文案（模板统一渲染）。 */
const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能继续'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

async function load() {
  const id = Number(props.id)
  const made = await run(async () => {
    post.value = await postsApi.getPost(id)
    comments.value = await postsApi.listComments(id)
    return true
  })
  // 分诊已经置好 failure；这里只补 capability 这个特殊分支（它要换成空态）。
  if (made === null && failure.value?.kind === 'capability') capabilityMissing.value = true
}

/**
 * 点赞：**先改本地、再发请求、失败回滚**。
 *
 * 乐观更新是必要的（点赞是最频繁的互动，等一次往返会让按钮"慢半拍"），
 * 但回滚同样必要 —— 否则一次 429 会让界面停在谎言的计数上，而下次刷新就对不上了。
 * 回滚值从服务端响应里取（权威计数），不从"减一"推：服务端才是真相。
 */
async function toggleLike() {
  if (!post.value || likingBusy.value) return
  if (!ensureLoggedInForWrite()) return

  const before = { liked: post.value.liked, likeCount: post.value.likeCount }
  // 显式取 1 / -1：三元里混 boolean 会让 TS 推断成 boolean + number。
  const delta = before.liked ? -1 : 1
  post.value.liked = !before.liked
  post.value.likeCount = before.likeCount + delta

  likingBusy.value = true
  const r = await run(() => postsApi.toggleLike(post.value!.id))
  likingBusy.value = false

  if (r === null) {
    // 失败：先退回原值，若服务端给了权威计数则优先用它。
    post.value.liked = before.liked
    post.value.likeCount = before.likeCount
    return
  }
  post.value.liked = r.liked
  post.value.likeCount = r.likeCount
}

async function addComment() {
  const c = draft.value.trim()
  if (!c || !post.value) return
  if (!ensureLoggedInForWrite()) return

  const made = await run(() => postsApi.addComment({ postId: post.value!.id, content: c }))
  if (made === null) return
  comments.value.push(made)
  post.value.commentCount += 1
  draft.value = ''
}

/** 删评论：服务端判"只能删自己的"（403 的文案就是它给的），前端不预判。 */
async function removeComment(c: Comment) {
  if (!post.value) return
  const made = await run(() => postsApi.deleteComment(c.id))
  if (made === null) return
  comments.value = comments.value.filter((x) => x.id !== c.id)
  post.value.commentCount = Math.max(0, post.value.commentCount - 1)
}

onMounted(load)
</script>

<template>
  <div class="page">
    <main class="body">
      <p v-if="failureText" class="alert" role="alert">{{ failureText }}</p>
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
          <button :class="{ liked: post.liked }" type="button" :disabled="likingBusy" @click="toggleLike">
            <BrandIcon v-if="likingBusy" name="load-spinner" decorative :size="14" />
            <span>{{ post.liked ? '已赞' : '赞' }} {{ post.likeCount }}</span>
          </button>
          <button type="button" @click="router.back()">返回</button>
        </div>
        <section class="comments">
          <h2>评论（{{ comments.length }}）</h2>
          <div v-for="c in comments" :key="c.id" class="cmt">
            <strong>{{ c.author.displayName }}</strong>
            <p>{{ c.content }}</p>
            <!-- 删除按钮只给自己的评论；但**真正能不能删由服务端判**（403），
                 这里的显隐只是少一次注定失败的往返。 -->
            <button v-if="isMine(c)" class="del" type="button" @click="removeComment(c)">删除</button>
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
.acts button:disabled { opacity: 0.6; cursor: progress; }
.comments { border-top: 1px solid var(--line); padding-top: 16px; }
.comments h2 { margin: 0 0 10px; font-size: 15px; }
.cmt { padding: 8px 0; border-bottom: 1px dashed var(--line); font-size: 13px; }
.cmt p { margin: 4px 0 0; white-space: pre-wrap; }
/* 删除自己的评论：低调但可点（红色 hover 才显形，避免一排红字抢焦点）。 */
.cmt .del { margin-top: 4px; padding: 2px 8px; border: 1px solid transparent; color: var(--dim); font-size: 11px; }
.cmt .del:hover { color: var(--danger); border-color: var(--danger); }
.add { display: flex; gap: 8px; margin-top: 12px; }
.add button { flex: none; padding: 8px 18px; background: var(--accent); color: var(--accent-ink); font-weight: 700; }
.add button:disabled { opacity: 0.5; }
.alert { color: var(--danger); }
.alert[role='alert'] { margin: 0; padding: 8px 10px; border: 1px solid var(--danger); background: rgba(255, 92, 92, 0.12); font-size: 12px; color: #ffdada; }
</style>
