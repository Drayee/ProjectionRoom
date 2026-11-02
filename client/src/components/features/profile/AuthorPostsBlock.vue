<script setup lang="ts">
/**
 * 他人档案的「TA 的帖子」内容块（票 #68）。免登录可读。
 *
 * # 为什么单独一个组件而不是塞进 UserPage.vue
 *
 * 档案页已经有两块同构的内容（帖 / 房）：各自有加载、空、错三态 + 无限滚动。
 * 把它们平铺进页面会让那个文件涨到一千行以上，而"两块内容的加载互不影响"
 * 这件事也会变成一堆散落的 ref（`postsLoading` / `roomsLoading` / …）。
 * 每块自带状态，页面只管按块序摆位置。
 *
 * # 为什么每条不画 AuthorRow
 *
 * 这一页的作者就是页主本人 —— 每行再放一遍头像和昵称，等于把同一个名字
 * 重复 N 次。`ui/AuthorRow` 的用途是"这条内容是谁写的"，而在这里答案是
 * 页面的标题。所以这里只画"内容本身"：标题 + 摘要 + 相对时间 + 计数。
 *
 * # 分页
 *
 * 与服务端 `GET /api/users/:id/posts?limit&offset` 一一对应，门面用 `page`（1 起）。
 * 滚动到底自动加载下一页；`IntersectionObserver` 不可用时（极老的浏览器 /
 * 测试环境）退化成"加载更多"按钮 —— 按钮始终存在，只是通常被观察器抢先触发。
 *
 * # 删帖（票 #70）
 *
 * 只在 `self` 为真时出现删除按钮 —— 这一块的 `userId` 在他人档案页是**别人**，
 * 而"能不能删"是服务端判的（I3）。前端不靠"这行是我写的"来隐藏，而是靠
 * "这一页是不是我的管理台"：他人档案页上根本不该有删除入口，连灰的都不该有
 * （那会让人以为点了会有用）。
 *
 * 删除是**乐观移除 + 失败回滚**：先在列表里摘掉，服务端 403/404/网络错时按原位置
 * 插回去并给一句人话。为什么不是"等服务端回来再移除"：那样点一下要等一整个往返，
 * 而删除是用户已经决定好的事，等待只是让界面显得卡。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import AsyncState from '../../ui/AsyncState.vue'
import Button from '../../ui/Button.vue'
import RelativeTime from '../../ui/RelativeTime.vue'
import * as postsApi from '../../../api/posts'
import * as usersApi from '../../../api/users'
import { formatCount } from '../../../utils/count'
import { isCapabilityUnavailable } from '../../../api/mode'
import { toHttpError } from '../../../api/http'
import type { Post } from '../../../types/posts'

const props = withDefaults(
  defineProps<{
    /** 作者 id。`/me` 传自己的 id，档案页传路由里的 id。 */
    userId: number
    /**
     * 块标题。
     *
     * 同一个组件被两个地方用（票 #69）：他人档案页说「TA 的帖子」，
     * `/me` 说「我的帖子」。**不是**让调用方自定义样式，只是换主语 ——
     * 所以空态文案也跟着主语变（见 `title` / `hint` 的计算）。
     */
    title?: string
    /** 主语：他人视角用「TA」，本人视角用「你」。只影响文案，不影响行为。 */
    self?: boolean
  }>(),
  { title: 'TA 的帖子', self: false },
)

const items = ref<Post[]>([])
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const errorText = ref('')
/** 整域未上线（真实实现撞上 NoRoute 的纯文本 404）。 */
const capabilityMissing = ref(false)

/** 三态判定：与帖子列表同一个优先级（能力降级 > 错误 > 载/空）。 */
const state = computed<'loading' | 'empty' | 'error' | 'capability' | 'ready'>(() => {
  if (capabilityMissing.value) return 'capability'
  if (errorText.value !== '') return 'error'
  if (loading.value) return 'loading'
  if (items.value.length === 0) return 'empty'
  return 'ready'
})

const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | undefined

/**
 * 请求序号：换人之后，上一个 id 的迟到响应必须被丢弃。
 *
 * 不这么做的症状是"点开 A 再点开 B，B 的列表先到、A 的随后把 B 盖掉"，
 * 而两次请求都很正常 —— 只靠"看响应内容"是查不出这种 bug 的。
 */
let seq = 0

function messageOf(err: unknown): string {
  return toHttpError(err).message
}

/** 取一页。`reset` 时从头开始（换页主 / 重试）。 */
async function load(reset: boolean): Promise<void> {
  if (props.userId <= 0) return
  const mine = ++seq
  if (reset) {
    loading.value = true
    errorText.value = ''
    capabilityMissing.value = false
    page.value = 1
    items.value = []
  } else {
    loadingMore.value = true
  }
  try {
    const result = await usersApi.listAuthorPosts(props.userId, { page: page.value })
    if (mine !== seq) return
    items.value = reset ? result.items : [...items.value, ...result.items]
    hasMore.value = result.hasMore
  } catch (err) {
    if (mine !== seq) return
    if (isCapabilityUnavailable(err)) {
      capabilityMissing.value = true
    } else {
      errorText.value = messageOf(err)
    }
  } finally {
    if (mine === seq) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

async function loadMore(): Promise<void> {
  if (!hasMore.value || loading.value || loadingMore.value) return
  page.value += 1
  await load(false)
}

function retry(): void {
  void load(true)
}

/**
 * 换人（同一个组件被复用到另一个 id）时重取。
 *
 * 用 `immediate: true` 让首次挂载也走这一条 —— 两个入口（挂载 + 换 id）
 * 共用同一段逻辑，不会出现"首次加载忘了调"这类不对称。
 */
watch(
  () => props.userId,
  () => {
    void load(true)
  },
  { immediate: true },
)

onMounted(() => {
  if (typeof IntersectionObserver === 'undefined' || !sentinel.value) return
  observer = new IntersectionObserver(
    (entries) => {
      if (entries.some((e) => e.isIntersecting)) void loadMore()
    },
    { rootMargin: '200px' },
  )
  observer.observe(sentinel.value)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = undefined
})

/** 摘要：与帖子列表同一套截断（72 字 + 省略号），超长正文不撑破布局。 */
function excerpt(content: string, max = 72): string {
  const flat = content.replace(/\s+/g, ' ').trim()
  return flat.length <= max ? flat : flat.slice(0, max) + '…'
}

// ---------- 删帖（票 #70） ----------

/** 正在删的那条（同一时刻只允许删一条：连点两下不该发两个请求）。 */
const deletingId = ref<number | null>(null)
/** 删除失败的人话提示（`notice` 与它分开：一个是中性结果，一个是错误）。 */
const deleteError = ref('')

/**
 * 乐观删除：先摘掉，失败插回原位。
 *
 * `index` 用来插回**原来的位置**而不是塞到末尾 —— 一条删失败的帖子跑到列表最后，
 * 会让人以为列表顺序变了（其实只是它被重排了）。
 */
async function remove(post: Post): Promise<void> {
  if (deletingId.value !== null) return
  const index = items.value.findIndex((p) => p.id === post.id)
  if (index === -1) return
  deletingId.value = post.id
  deleteError.value = ''
  items.value.splice(index, 1)
  try {
    await postsApi.deletePost(post.id)
  } catch (err) {
    // 404 = 服务端已经没有它了（比如另一个标签页删过）：**不回滚**，
    // 因为把它插回列表等于显示一条不存在的帖子。
    const status = toHttpError(err).status
    if (status !== 404) {
      items.value.splice(index, 0, post)
    }
    deleteError.value =
      status === 403
        ? '只能删除自己的帖子'
        : status === 404
          ? '这帖已经不存在了'
          : toHttpError(err).message
  } finally {
    deletingId.value = null
  }
}
</script>

<template>
  <section class="block" aria-labelledby="author-posts-head">
    <div class="block-head">
      <h2 id="author-posts-head" class="block-title">{{ props.title }}</h2>
      <span v-if="state === 'ready'" class="dim small">{{ items.length }} 篇</span>
    </div>

    <AsyncState
      v-if="state !== 'ready'"
      :state="state === 'capability' ? 'capability' : state"
      :title="
        state === 'empty'
          ? props.self
            ? '你还没发过帖'
            : 'TA 还没发过帖'
          : state === 'error'
            ? '帖子没读出来'
            : state === 'capability'
              ? '帖子分区尚未上线'
              : undefined
      "
      :hint="
        state === 'empty'
          ? props.self
            ? '说说今晚想一起看什么 —— 第一帖可能就是你写的。'
            : '等 TA 发第一条，或者去帖子广场看看别人在聊什么。'
          : state === 'error'
            ? errorText
            : undefined
      "
      :retryable="state === 'error'"
      icon="chat"
      @retry="retry"
    >
      <RouterLink
        v-if="state === 'empty'"
        class="cta"
        :to="props.self ? { name: 'post-editor' } : { name: 'posts' }"
      >{{ props.self ? '去发第一帖' : '去帖子广场' }}</RouterLink>
    </AsyncState>

    <template v-else>
      <p v-if="deleteError" class="alert" role="alert">{{ deleteError }}</p>

      <ul class="rows">
        <li v-for="post in items" :key="post.id" class="row">
          <RouterLink class="row-link" :to="{ name: 'post-detail', params: { id: String(post.id) } }">
            <strong class="row-title">{{ post.title }}</strong>
            <span class="row-excerpt dim">{{ excerpt(post.content) }}</span>
            <span class="row-meta dim small">
              <RelativeTime :value="post.createdAt" />
              <template v-if="formatCount(post.likeCount)">
                <span aria-hidden="true">·</span>
                <span>赞 {{ formatCount(post.likeCount) }}</span>
              </template>
              <template v-if="formatCount(post.commentCount)">
                <span aria-hidden="true">·</span>
                <span>评论 {{ formatCount(post.commentCount) }}</span>
              </template>
            </span>
          </RouterLink>
          <!-- 删除入口只在 /me（self）出现：他人档案页上连灰的都不该有。 -->
          <div v-if="props.self" class="row-acts">
            <Button
              variant="danger"
              size="xs"
              data-testid="delete-post"
              :loading="deletingId === post.id"
              :disabled="deletingId !== null"
              :aria-label="`删除帖子：${post.title}`"
              @click="remove(post)"
            >
              删除
            </Button>
          </div>
        </li>
      </ul>

      <div ref="sentinel" class="sentinel" aria-hidden="true" />
      <div class="more-row">
        <button v-if="hasMore && !loadingMore" type="button" class="more-btn" @click="loadMore">
          加载更多
        </button>
        <p v-else-if="loadingMore" class="dim small">加载更多…</p>
        <p v-else class="dim small">— 到底了 —</p>
      </div>
    </template>
  </section>
</template>

<style scoped>
.block {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 10px;
  border-top: 1px solid var(--line);
  padding-top: 16px;
}
.block-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
}
.block-title {
  margin: 0;
  font-size: 15px;
}
.rows {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.row {
  border: 1px solid var(--line);
  background: var(--surface);
  /* 删除按钮在右侧独立一列：它不能落在 `.row-link` 里面（那是整块链接）。 */
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: start;
  gap: 8px;
  padding-right: 12px;
}
.row-acts {
  padding-top: 10px;
}
.alert {
  margin: 0;
  padding: 8px 12px;
  border: 1px solid var(--danger);
  color: var(--danger);
  font-size: 13px;
}
.row-link {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 14px;
  color: inherit;
  text-decoration: none;
}
.row-link:hover {
  border-color: var(--accent);
  text-decoration: none;
}
.row-link:hover .row-title {
  color: var(--accent);
}
.row-title {
  font-size: 14px;
  overflow-wrap: anywhere;
}
.row-excerpt {
  font-size: 12px;
  line-height: 1.6;
  overflow-wrap: anywhere;
}
.row-meta {
  display: flex;
  align-items: baseline;
  gap: 6px;
  flex-wrap: wrap;
}
.sentinel {
  height: 1px;
}
.more-row {
  display: flex;
  justify-content: center;
  padding: 4px 0;
}
.more-btn {
  padding: 6px 16px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
  background: transparent;
}
.more-btn:hover {
  color: var(--text);
  border-color: var(--accent);
}
.cta {
  display: inline-flex;
  align-items: center;
  padding: 6px 16px;
  border: 1px solid var(--line);
  color: var(--dim);
  font-size: 13px;
}
.cta:hover {
  color: var(--text);
  border-color: var(--accent);
  text-decoration: none;
}
.dim {
  color: var(--dim);
}
.small {
  font-size: 12px;
}
</style>
