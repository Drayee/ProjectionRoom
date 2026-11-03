<script setup lang="ts">
/**
 * 帖子详情（/posts/:id）。#26 切真：写操作按 401/403/429 分诊，点赞做乐观更新 +
 * 失败回滚，评论可删（仅作者，服务端判）。
 *
 * 票 #63 之后本页与列表页共用三件事，不再各写一份：
 *   - 作者行 → `ui/AuthorRow`（detail 档给正文头、comment 档给评论行）；
 *   - 空/载/错 → `ui/AsyncState`；
 *   - 点赞 → `usePostLike`（乐观更新 + 失败回滚 + 单条在途锁）。
 *
 * 为什么详情页也必须换成共用件：原先它的点赞是**独立一份**乐观更新实现，
 * 而列表页现在也有了点赞。两份实现意味着下次改回滚边界只改一处，另一处继续把计数
 * 停在谎言上 —— 这种缺陷只在服务端开始 429 时才暴露。
 *
 * 时间显示走 `ui/RelativeTime.vue`（票 #60），由 AuthorRow 内部渲染。
 */
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ActionBar from '../components/ui/ActionBar.vue'
import AsyncState from '../components/ui/AsyncState.vue'
import AuthorRow from '../components/ui/AuthorRow.vue'
import Button from '../components/ui/Button.vue'
import MarkdownView from '../components/ui/MarkdownView.vue'
import * as postsApi from '../api/posts'
import { useApiFailure, ensureLoggedInForWrite } from '../components/features/common/useApiFailure'
import { useFavorite } from '../components/features/common/useFavorite'
import { usePostLike } from '../components/features/common/usePostLike'
import { useAuthStore } from '../stores/auth'
import type { Comment, Post } from '../types/posts'

const props = defineProps<{ id: string }>()
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { failure, run } = useApiFailure(() => route.fullPath)
const { toggle, isBusy, toast } = usePostLike()
const { setFavorite, isBusy: isFavBusy, toast: favToast } = useFavorite()

/** 轻提示合成（与列表页同一理由：两个 hook 各有一条，界面上只该有一句）。 */
const notice = computed(() => toast.value || favToast.value)

const post = ref<Post | null>(null)
const comments = ref<Comment[]>([])
const draft = ref('')
/** 该分区后端未上线：渲染降级空态，而不是「帖子不存在」。 */
const capabilityMissing = ref(false)

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

/** 详情区该渲染哪一态（`ready` 时直接渲染正文，不多套空壳）。 */
const pageState = computed<'loading' | 'error' | 'capability' | 'ready'>(() => {
  if (capabilityMissing.value) return 'capability'
  if (failureText.value) return 'error'
  if (!post.value) return 'loading'
  return 'ready'
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

/** 点赞：共用实现（乐观更新 + 失败回滚 + 在途锁），这里只做空值守卫。 */
async function toggleLike() {
  if (!post.value) return
  await toggle(post.value)
}

/** 收藏（票 #64）：与列表页共用 `useFavorite`，这里只做空值守卫。 */
function onFavorite() {
  if (!post.value) return
  const target = post.value
  void setFavorite('post', String(target.id), !target.favorited, (value) => {
    target.favorited = value
  })
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
      <AsyncState
        v-if="pageState !== 'ready'"
        :state="pageState"
        :title="
          pageState === 'capability' ? '帖子分区后端尚未上线' : pageState === 'error' ? '没能加载出这篇帖子' : '加载中'
        "
        :hint="
          pageState === 'capability'
            ? '上线后这里是单篇帖子：正文、点赞与扁平评论。'
            : pageState === 'error'
              ? failureText
              : ''
        "
        :retryable="pageState === 'error' || pageState === 'capability'"
        @retry="load"
      />

      <template v-else-if="post">
        <!-- 头图（票 #78）：与列表卡封面是同一张图。两端都写 `data-shared="post-cover:<id>"`，
             匹配只按这个值 —— 所以从 `/me` 的帖子块点进来也一样能飞。
             它是**新增**的展示位（详情页此前没有封面图）：没有封面就不渲染，不留空框。 -->
        <img
          v-if="post.images.length"
          class="hero-cover"
          :src="post.images[0]"
          :alt="`${post.title} 封面`"
          :data-shared="`post-cover:${post.id}`"
        />
        <h1 class="title">{{ post.title }}</h1>
        <!-- 正文头：作者是主角，用 detail 档（头像 md、昵称 15px、时间另起一行）。 -->
        <AuthorRow
          class="post-author"
          :author="post.author"
          :created-at="post.createdAt"
          :self="auth.profile?.id === post.authorId"
          size="detail"
        />
        <MarkdownView class="content" :source="post.content" label="帖子正文" />
        <!-- 动作条与列表页同款（赞 → 收藏 → 评论），但详情页自己就是评论区，
             所以把"跳到评论"摘掉（showComment=false）。 -->
        <div class="acts">
          <ActionBar
            :liked="post.liked"
            :like-count="post.likeCount"
            :favorited="post.favorited"
            :comment-count="post.commentCount"
            :like-busy="isBusy(post.id)"
            :favorite-busy="isFavBusy('post', String(post.id))"
            :show-comment="false"
            @like="toggleLike"
            @favorite="onFavorite"
          />
          <Button variant="ghost" @click="router.back()">返回</Button>
        </div>
        <!-- id="comments" 是列表页动作条「评论」按钮的落点（它 push 时带 #comments）。 -->
        <section id="comments" class="comments">
          <h2>评论（{{ comments.length }}）</h2>
          <p v-if="comments.length === 0" class="dim empty">还没有评论 —— 说点什么，作者会看到。</p>
          <div v-for="c in comments" :key="c.id" class="cmt">
            <!-- 评论行：comment 档（头像 xs、昵称 13px、时间同行）。这里没有外层链接，
                 所以头像与昵称可点（进档案）。 -->
            <AuthorRow
              :author="c.author"
              :created-at="c.createdAt"
              :self="auth.profile?.id === c.authorId"
              size="comment"
            />
            <p class="cmt-body">{{ c.content }}</p>
            <!-- 删除按钮只给自己的评论；但**真正能不能删由服务端判**（403），
                 这里的显隐只是少一次注定失败的往返。 -->
            <Button v-if="isMine(c)" variant="danger" size="xs" class="del" @click="removeComment(c)">删除</Button>
          </div>
          <form class="add" @submit.prevent="addComment">
            <input v-model="draft" placeholder="说点什么…" />
            <Button type="submit" variant="primary" :disabled="!draft.trim()">发送</Button>
          </form>
        </section>
      </template>
    </main>

    <!-- 轻提示（点赞失败 / 收藏占位）：写操作失败不把整页换成错误态。 -->
    <p v-if="notice" class="toast" role="status" data-testid="action-toast">{{ notice }}</p>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; position: relative; }
.body { flex: 1; width: min(760px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 14px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(26px, 5vw, 38px); }
/* 头图（票 #78）：与列表卡封面**同样的圆角与边框**，这是 morph 两端唯一需要对齐的东西
   （决议 #45 §3 第 4 条：只共享外观契约，不共享组件实例）。裁切用 object-fit: cover ——
   卡片封面是窄条、这里是宽幅，同一张图要在两种比例下都不变形。 */
.hero-cover {
  display: block;
  width: 100%;
  margin: 0;
  aspect-ratio: 16 / 9;
  object-fit: cover;
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.content { margin: 0; }
/* 动作行：外观交给 ui/ActionBar 与 ui/Button，页面只管排布。
   注意 hover 与选中态由组件负责，页面不再重写状态色。 */
.acts { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.comments { border-top: 1px solid var(--line); padding-top: 16px; }
.comments h2 { margin: 0 0 10px; font-size: 15px; }
.empty { margin: 0 0 10px; font-size: 13px; }
.cmt { padding: 10px 0; border-bottom: 1px dashed var(--line); }
.cmt-body { margin: 6px 0 0; font-size: 13px; white-space: pre-wrap; overflow-wrap: anywhere; }
/* 删除自己的评论：低调但可点（红色 hover 才显形，避免一排红字抢焦点）。
   用 xs 档：视觉小（11px 字），命中区仍是 32px 高（票 #43 §6）。 */
.cmt .del { margin-top: 6px; border-color: transparent; }
.add { display: flex; gap: 8px; margin-top: 12px; }
.add input { flex: 1; min-width: 0; }
.add button { flex: none; }

/* 轻提示：与列表页同一形态（固定底部居中，不推动布局）。 */
.toast {
  position: fixed;
  left: 50%;
  bottom: 24px;
  transform: translateX(-50%);
  z-index: 20;
  margin: 0;
  padding: 10px 16px;
  border: 1px solid var(--line-strong);
  background: var(--bg-mid);
  box-shadow: var(--elev-2);
  color: var(--text);
  font-size: 13px;
  max-width: min(520px, calc(100vw - 40px));
}
</style>
