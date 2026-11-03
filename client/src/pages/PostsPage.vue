<script setup lang="ts">
/**
 * 帖子列表（/posts）。票 #15 定稿形态：瀑布流 masonry + 无限滚动 + 排序 pill。
 *
 * 票 #63 把卡片拆成三层（此前整张卡是一个 `<RouterLink>`，作者/时间/计数全挤在底部一行里）：
 *
 * ```
 * ┌─────────────────────────────┐
 * │ AuthorRow（头像 昵称 时间）   │  ← 谁写的
 * │ a.card-link（封面 标题 摘要） │  ← 点这里进详情（唯一的导航区）
 * │ ActionBar（赞 收藏 评论）     │  ← 能做什么 + 多少
 * └─────────────────────────────┘
 * ```
 *
 * 为什么必须拆：在 `<a>` 里放 `<button>` 是**无效 HTML**，浏览器会把按钮从可点区域里
 * 摘掉（或者点按钮时同时触发导航）。真机脚本 verify-authorrow.mjs 的 R2c 直接钉住这条。
 *
 * 三态（载/空/错/能力降级）统一走 `ui/AsyncState`，点赞走共用的 `usePostLike`
 * （乐观更新 + 失败回滚 + 单条在途锁，与详情页同一份实现）。
 *
 * 票 #66 加了第三个 pill「关注」：
 *
 *   - 选中态进 URL（`?sort=`），键名唯一源是 `utils/postsRoute.ts`；
 *   - 它**需要登录**。游客点它**不被弹走** —— 列表区就地渲染登录引导（决议 #52 §6），
 *     理由是"读动作被弹走最烦"：用户想看关注流，却被扔到登录页，回来还得自己点回来；
 *   - 已登录但 0 关注时给"去找人"的深链（`/friends?focus=search`），
 *     不搞"暂无更新"那种假空态（决议 §7：不设新鲜度门槛）。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { RouterLink } from 'vue-router'
import ActionBar from '../components/ui/ActionBar.vue'
import AsyncState from '../components/ui/AsyncState.vue'
import AuthorRow from '../components/ui/AuthorRow.vue'
import BrandIcon from '../components/ui/BrandIcon.vue'
import Button from '../components/ui/Button.vue'
import * as postsApi from '../api/posts'
import { useApiFailure } from '../components/features/common/useApiFailure'
import { useFavorite } from '../components/features/common/useFavorite'
import { usePostLike } from '../components/features/common/usePostLike'
import { useAuthStore } from '../stores/auth'
import { POSTS_SORT_QUERY, parsePostsSort, postsPath, sortNeedsLogin, type PostsSort } from '../utils/postsRoute'
import { findPeoplePath } from '../utils/friendsRoute'
import type { Post } from '../types/posts'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { failure, run } = useApiFailure(() => route.fullPath)
const { toggle, isBusy, toast } = usePostLike()
const { setFavorite, isBusy: isFavBusy, toast: favToast } = useFavorite()

/**
 * 页面上那条轻提示的文案。
 *
 * 点赞与收藏各有一个 `useXxx` 实例，各自持有一条提示 —— 这里合成一条显示：
 * 同一时刻只该有一句话，两个 `<p>` 会叠在一起互相盖不住。
 * 优先级给点赞（更常用的那个），但它本来也不会同时出现（在途锁各管各的键）。
 */
const notice = computed(() => toast.value || favToast.value)

const posts = ref<Post[]>([])
/**
 * 当前排序。**URL 是唯一真源**（票 #66 / 决议 #52 §8）。
 *
 * 用 computed 读 URL 而不是用 ref 存一份：两份状态一定会漂 ——
 * 症状是"后退键回到上一个 pill，但列表还是新的那个"。
 */
const sort = computed<PostsSort>(() => parsePostsSort(route.query[POSTS_SORT_QUERY]))
const busy = ref(false)
const loadingMore = ref(false)
const page = ref(1)
const hasMore = ref(false)
/**
 * 后端该域未上线（`VITE_MOCK_POSTS` 翻成真实实现、而 `/api/posts/*` 还没注册）。
 * 与「加载失败」分开：前者是能力未上线，后者才是错误。
 */
const capabilityMissing = ref(false)

/**
 * 关注流需要登录（票 #66 / 决议 #52 §6）。
 *
 * 刻意**不是** `auth.redirectToLogin()`：读动作被弹走最烦。这里只在列表区
 * 就地渲染一段登录引导，URL 保持 `?sort=following` —— 用户登录回来还在这个 pill 上。
 */
const needsLogin = computed(() => sortNeedsLogin(sort.value) && !auth.isLoggedIn && !auth.restoring)

/** 失败态的一行文案（分诊结果，见 useApiFailure）。 */
const failureText = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'unauthorized') return '需要登录后才能继续'
  if (f.kind === 'forbidden' || f.kind === 'ratelimited' || f.kind === 'error') return f.message
  return ''
})

/** AsyncState 的非 ready 态（`ready` 时直接渲染瀑布流，不多套一层空壳）。 */
type ListState = 'loading' | 'empty' | 'error' | 'capability' | 'login'

/**
 * 列表区该渲染哪一态。
 *
 * 顺序刻意是「登录引导 → 能力降级 → 错误 → 加载 → 空」：
 *
 *   - 登录引导排最前：游客看关注流时**根本没有请求可发**，让它落到 error/empty
 *     都是在描述一件没发生过的事；
 *   - 能力降级与错误都必须在**有旧数据**时也优先显示，否则用户会以为
 *     "刷新成功了，只是变少了"。
 */
const listState = computed<ListState | 'ready'>(() => {
  if (needsLogin.value) return 'login'
  if (capabilityMissing.value) return 'capability'
  if (failureText.value) return 'error'
  if (busy.value) return 'loading'
  if (posts.value.length === 0) return 'empty'
  return 'ready'
})

/**
 * 空态文案：**关注流空**与**全站没有帖子**必须分开说（决议 #52 §7）。
 *
 * 关注流空时用户要做的事是"去找人关注"，而不是"去发第一帖" ——
 * 后者会让一个刚注册的人以为"关注流里应该有我的帖子"。
 */
const emptyCopy = computed(() => {
  if (sort.value === 'following') {
    return {
      title: '还没有关注任何人',
      hint: '关注是单向的，不需要对方同意。去好友页找一个人，点「关注」就能在这里看到 TA 的帖子。',
    }
  }
  return {
    title: '这里还没有帖子',
    hint: '第一条帖子可能就是你写的 —— 说说今晚想一起看什么。',
  }
})

const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | undefined

/** 列表卡片摘要：去掉 Markdown 标记，压成单行短句。 */
function excerpt(content: string, max = 72): string {
  const plain = content
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/[#>*_`~\[\]()!|-]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
  return plain.length > max ? `${plain.slice(0, max)}…` : plain
}

async function load() {
  // 游客点进关注流：不发请求（发出去必然 401，而那个 401 会被分诊成"错误"，
  // 把一段本可以就地展示的登录引导变成一句吓人的红字）。
  if (needsLogin.value) return
  if (busy.value) return
  busy.value = true
  const r = await run(() => postsApi.listPosts({ sort: sort.value, page: 1 }))
  busy.value = false
  if (r === null) {
    // 整域 404 → 降级空态（把已渲染的列表清掉，否则空态与旧内容同屏很怪）。
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      posts.value = []
      hasMore.value = false
    }
    return
  }
  posts.value = r.items
  page.value = 1
  hasMore.value = r.hasMore
  capabilityMissing.value = false
}

async function loadMore() {
  if (busy.value || loadingMore.value || !hasMore.value) return
  loadingMore.value = true
  const next = page.value + 1
  const r = await run(() => postsApi.listPosts({ sort: sort.value, page: next }))
  loadingMore.value = false
  if (r === null) {
    if (failure.value?.kind === 'capability') {
      capabilityMissing.value = true
      hasMore.value = false
    }
    return
  }
  posts.value = posts.value.concat(r.items)
  page.value = next
  hasMore.value = r.hasMore
}

function switchSort(next: PostsSort) {
  if (sort.value === next) return
  // 只改 URL —— `watch(sort)` 负责重取（票 #66：URL 是唯一真源）。
  //
  // 这样做的好处是**后退键免费生效**：用户从"关注"点回"最新"再按后退，
  // 地址栏与列表会一起回去；如果这里同时 `void load()`，后退就会
  // 变成"地址变了、列表还是新的"（两份状态各走各的）。
  void router.replace(postsPath({ sort: next }))
}

/**
 * 动作条上的「评论」按钮：进详情页。
 *
 * 卡片上已经没有整块链接了，所以评论按钮**必须自己**导航 —— 否则它就是一个点了
 * 什么都不发生的按钮（比没有更糟）。带 `#comments` 让浏览器滚到评论区。
 */
function openPost(id: number) {
  void router.push({ name: 'post-detail', params: { id: String(id) }, hash: '#comments' })
}

/**
 * 收藏（票 #64）：乐观更新 + 失败回滚 + 在途锁，与点赞同一套纪律。
 *
 * 与点赞的差别只有"作用对象"：点赞直接改 `p` 这个响应式对象，收藏则要按
 * `(type, targetId)` 定位，所以写回函数自己找那一行改。
 */
function onFavorite(post: Post) {
  void setFavorite('post', String(post.id), !post.favorited, (value) => {
    post.favorited = value
  })
}

onMounted(() => {
  void load()
  if (typeof IntersectionObserver !== 'undefined' && sentinel.value) {
    observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) void loadMore()
      },
      { rootMargin: '200px' },
    )
    observer.observe(sentinel.value)
  }
})

/**
 * 排序变了就重取（票 #66）。
 *
 * 盯 `sort`（也就是 URL）而不是盯点击：这样后退键、手改地址栏、
 * 从别处跳进来三种来源都走同一条路径。
 *
 * 关注流对游客不发请求（`load()` 内部早退），所以"游客切到关注流"不会打出
 * 一个必然 401 的请求 —— 那个 401 只该在真的没登录却硬发请求时出现。
 */
watch(sort, () => {
  posts.value = []
  hasMore.value = false
  page.value = 1
  failure.value = null
  if (needsLogin.value) return
  void load()
})

/** 登录态翻转后补一次（游客点了「去登录」、在新标签页登录回来时用得上）。 */
watch(
  () => auth.isLoggedIn,
  (loggedIn) => {
    if (!loggedIn || !sortNeedsLogin(sort.value)) return
    if (posts.value.length === 0) void load()
  },
)

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = undefined
})
</script>

<template>
  <div class="page">
    <main class="body">
      <div class="head">
        <h1 class="title">帖子</h1>
        <RouterLink class="new" :to="{ name: 'post-editor' }">
          <BrandIcon name="upload" decorative :size="14" />
          发帖
        </RouterLink>
      </div>
      <!-- 排序标签页：外壳是 ui/Button（票 #62 首批替换），选中态用组件暴露的变量接上，
           页面不重写状态色。三个 pill 的选中态都来自 URL（票 #66）。 -->
      <div class="tabs" role="tablist">
        <Button
          variant="ghost"
          class="tab"
          role="tab"
          data-sort="latest"
          :aria-selected="sort === 'latest'"
          :class="{ on: sort === 'latest' }"
          @click="switchSort('latest')"
        >
          最新
        </Button>
        <Button
          variant="ghost"
          class="tab"
          role="tab"
          data-sort="hot"
          :aria-selected="sort === 'hot'"
          :class="{ on: sort === 'hot' }"
          @click="switchSort('hot')"
        >
          热门
        </Button>
        <!-- 「关注」对游客**可见可点**（决议 #52 §6）：点进来看到的是就地登录引导，
             而不是被弹到登录页。 -->
        <Button
          variant="ghost"
          class="tab"
          role="tab"
          data-sort="following"
          :aria-selected="sort === 'following'"
          :class="{ on: sort === 'following' }"
          @click="switchSort('following')"
        >
          关注
        </Button>
      </div>

      <!-- 三态统一出口（票 #63）。空态带一个行动，而不是一句干巴巴的说明；
           关注流空时那个行动是"去找人"（票 #66 / 决议 #52 §7）。 -->
      <AsyncState
        v-if="listState !== 'ready'"
        :state="listState === 'login' ? 'empty' : listState"
        :title="
          listState === 'login'
            ? '登录后可看关注流'
            : listState === 'capability'
              ? '帖子分区后端尚未上线'
              : listState === 'error'
                ? '没能加载出帖子'
                : listState === 'empty'
                  ? emptyCopy.title
                  : '加载中'
        "
        :hint="
          listState === 'login'
            ? '关注流只显示你关注的人发的帖子。登录之后就能看到，不需要对方同意。'
            : listState === 'capability'
              ? '上线后这里是瀑布流帖子列表：最新 / 热门 / 关注排序、封面与评论计数。'
              : listState === 'error'
                ? failureText
                : listState === 'empty'
                  ? emptyCopy.hint
                  : ''
        "
        :icon="listState === 'login' ? 'users-group' : 'info'"
        :retryable="listState === 'error' || listState === 'capability'"
        @retry="load"
      >
        <!-- 登录引导：留在原地，URL 不动 —— 登录回来还停在「关注」这个 pill 上。 -->
        <RouterLink v-if="listState === 'login'" class="empty-cta" :to="{ name: 'login', query: { redirect: route.fullPath } }">
          <Button variant="primary" size="sm">去登录</Button>
        </RouterLink>
        <!-- 关注流为空：深链到好友页并聚焦页头搜索框（#50 已定搜索是状态不是去处）。 -->
        <RouterLink v-else-if="listState === 'empty' && sort === 'following'" class="empty-cta" :to="findPeoplePath()">
          <Button variant="primary" size="sm">去找人关注</Button>
        </RouterLink>
        <RouterLink v-else-if="listState === 'empty'" class="empty-cta" :to="{ name: 'post-editor' }">
          <Button variant="primary" size="sm">去发第一帖</Button>
        </RouterLink>
      </AsyncState>

      <div v-else class="masonry">
        <article v-for="p in posts" :key="p.id" class="post-card">
          <!-- 作者行：谁写的、什么时候写的。它**不在**链接里，所以昵称与头像可以点进档案
               （那是与"点卡片进帖子"不同的落点）。 -->
          <AuthorRow
            class="card-author"
            :author="p.author"
            :created-at="p.createdAt"
            :self="auth.profile?.id === p.authorId"
            size="compact"
          />

          <!-- 唯一的导航区：封面 + 标题 + 摘要。动作条刻意在它**外面**。 -->
          <RouterLink class="card-link" :to="{ name: 'post-detail', params: { id: String(p.id) } }">
            <!-- data-shared（票 #78）：对端是详情页头图。匹配只按这个值，
                 不写死"列表→详情"的页对 —— 从 /me 的帖子块点进来走同一条规则。 -->
            <img
              v-if="p.images.length"
              class="cover"
              :src="p.images[0]"
              :alt="`${p.title} 封面`"
              loading="lazy"
              :data-shared="`post-cover:${p.id}`"
            />
            <h2 class="card-title">{{ p.title }}</h2>
            <p v-if="p.content" class="excerpt dim">{{ excerpt(p.content) }}</p>
          </RouterLink>

          <ActionBar
            class="card-actions"
            :liked="p.liked"
            :like-count="p.likeCount"
            :comment-count="p.commentCount"
            :favorited="p.favorited"
            :like-busy="isBusy(p.id)"
            :favorite-busy="isFavBusy('post', String(p.id))"
            @like="toggle(p)"
            @favorite="onFavorite(p)"
            @comment="openPost(p.id)"
          />
        </article>
      </div>

      <div ref="sentinel" class="sentinel" aria-hidden="true" />
      <p v-if="loadingMore" class="dim">加载更多…</p>
      <p v-else-if="!hasMore && posts.length > 0" class="dim end">— 到底了 —</p>
    </main>

    <!-- 轻提示（点赞失败 / 收藏占位）：写操作的失败绝不把整页换成错误态。 -->
    <p v-if="notice" class="toast" role="status" data-testid="action-toast">{{ notice }}</p>
  </div>
</template>

<style scoped>
.page { min-height: 100vh; display: flex; flex-direction: column; position: relative; }
.body { flex: 1; width: min(880px, 100%); margin: 0 auto; padding: 32px 20px 64px; display: flex; flex-direction: column; gap: 16px; }
.head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.title { margin: 0; font-family: var(--font-glitch); font-size: clamp(32px, 6vw, 48px); }
.new { display: inline-flex; align-items: center; gap: 5px; padding: 8px 14px; border: 1px solid var(--accent); color: var(--accent); font-size: 13px; }
.new:hover { background: var(--state-hover); text-decoration: none; }
.tabs { display: flex; gap: 2px; }
.tab { --ui-btn-border-color: transparent; padding: 6px 16px; border-bottom: 2px solid transparent; border-radius: 0; }
.tab.on { --ui-btn-fg: var(--text); --ui-btn-fg-hover: var(--text); border-bottom-color: var(--accent); }

/* 瀑布流：CSS 多列，卡片 break-inside 避免被拆列 */
.masonry { columns: 2 320px; column-gap: 14px; }

/*
 * 卡片：三个块竖排（作者行 / 链接区 / 动作条）。
 *
 * 边框与背景留在 article 上（而不是链接上）：整张卡看起来是一块，
 * 但只有中间的链接区可点 —— 这是"卡不再是一整块链接"的视觉落点。
 */
.post-card {
  display: flex;
  flex-direction: column;
  gap: 10px;
  break-inside: avoid;
  margin-bottom: 14px;
  border: 1px solid var(--line);
  background: var(--surface);
  padding: 14px;
  transition: border-color var(--dur-micro) var(--ease), box-shadow var(--dur-micro) var(--ease);
}

/* hover 故障：RGB 分离外发光（静态，不逐帧重绘）。整卡一起亮，
   因为作者行与动作条都属于这张卡；链接区自己另有焦点环。 */
.post-card:hover {
  border-color: var(--accent);
  box-shadow: 3px 0 0 var(--glitch-r), -3px 0 0 var(--glitch-p), 0 8px 24px rgba(180, 108, 255, 0.25);
}

.card-link {
  display: block;
  color: inherit;
  text-decoration: none;
  min-width: 0;
}

.card-link:hover { text-decoration: none; }

.card-link:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}

.card-title { margin: 0 0 6px; font-size: 15px; }
.cover { display: block; width: 100%; margin-bottom: 10px; border: 1px solid var(--line); border-radius: var(--radius); }
.excerpt { margin: 0; font-size: 13px; }

/* 动作条贴底：卡片高度不一（瀑布流），让动作条始终在最下方。 */
.card-actions { margin-top: auto; }

.sentinel { height: 1px; }
.end { text-align: center; }

.empty-cta { align-self: flex-start; text-decoration: none; }

/* 轻提示：固定在视口底部居中，不推动布局（推动布局会让"回滚"看起来像页面抖了一下）。 */
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

/* 降级：只留颜色变化，位移类动效全部取消。 */
@media (prefers-reduced-motion: reduce) {
  .post-card { transition: border-color var(--dur-micro) var(--ease); }
}
</style>
