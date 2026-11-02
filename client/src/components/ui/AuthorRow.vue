<script setup lang="ts">
/**
 * AuthorRow —— 「谁写的、什么时候写的」这一行的唯一出口（票 #63，契约见决议 #44）。
 *
 * ## 它替换掉了什么
 *
 * 帖子列表的卡片底部原先是这一串手写插值：
 *
 * ```
 * {{ p.author.displayName }} · <RelativeTime/> · 赞 {{ p.likeCount }} · 评论 {{ p.commentCount }}
 * ```
 *
 * 那串东西把**四件不同的事**压进一行（作者、时间、两个计数），于是：
 *   - 头像没地方放（作者只剩一个名字）；
 *   - 时间与计数抢同一行，窄屏只能折行成两段乱码；
 *   - 计数没有单位（12345 就真的印 12345），也没有「0 就不显示」的规则。
 *
 * 现在拆成三层：`AuthorRow`（谁+何时）/ `ActionBar`（能做什么+多少）/ 卡片链接区（标题+摘要）。
 *
 * ## 三种尺寸
 *
 * | size | 用在哪 | 头像 | 昵称 | 时间 |
 * | :--- | :--- | :--- | :--- | :--- |
 * | `compact` | 列表卡片 | sm | 13px | 12px，与昵称同一行 |
 * | `detail` | 帖子详情头部 | md | 15px | 12px，另起一行（详情里作者是主角） |
 * | `comment` | 评论行 | xs | 13px | 12px，与昵称同一行 |
 *
 * 尺寸是**契约的一部分**（真机脚本按 `author-row--detail` / `author-row--comment` 找），
 * 所以类名固定，不接受调用方自定义。
 *
 * ## 分层（决议 #44 §0）
 *
 * `ui/` = 纯展示：不读 store、不 import 路由模块。所以「这是不是我本人」由调用方判断，
 * 「点昵称去哪」也由调用方用 `interactive` 控制 —— 组件自己不猜。
 *
 * ## 嵌套链接处必须传 `interactive=false`
 *
 * 列表卡片整块是 `<RouterLink>`（点标题进帖子），作者行嵌在里面。链接套链接是无效 HTML，
 * 浏览器会把内层链接从可点区域里摘掉，症状是"点头像没反应"。所以卡片里传 false，
 * 详情页/评论行传 true（那里没有外层链接，点头像真的能进档案）。
 */
import { computed } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import RelativeTime from './RelativeTime.vue'
import UserAvatar from './UserAvatar.vue'

/** 作者行尺寸档位（契约见决议 #44）。 */
export type AuthorRowSize = 'compact' | 'detail' | 'comment'

const props = withDefaults(
  defineProps<{
    /**
     * 作者。
     *
     * 只要求 `id` + `displayName`（与 `ui/UserAvatar` 同一口径）。`username` 与 `role`
     * 可选：帖子域返回的作者对象没有这两个字段，那就只显示昵称 —— 不编一个 `@xxx` 出来。
     */
    author: { id: number; displayName: string; username?: string; role?: string }
    /** 发布时间（RFC3339）。不传就不渲染时间位（例如"最近活跃"另行排版的地方）。 */
    createdAt?: string
    /** 尺寸档位。 */
    size?: AuthorRowSize
    /**
     * 是否显示 `@username`。
     *
     * 默认 `compact` 不显示、其余显示（列表里一行要塞下昵称+时间，再加 handle 会挤）；
     * 显式传值可以覆盖。作者对象没有 `username` 时无论如何都不显示。
     */
    showHandle?: boolean
    /**
     * 昵称与头像是否可点（进档案）。
     *
     * 默认 true。**嵌在别的链接里时必须传 false**（见文件头的说明）。
     */
    interactive?: boolean
    /** 这是不是当前登录用户本人（决定点头像去 `/me` 还是 `/user/:id`）。 */
    self?: boolean
  }>(),
  { createdAt: '', size: 'compact', showHandle: undefined, interactive: true, self: false },
)

/** 头像尺寸跟着档位走（compact 用 sm 而不是 xs：列表里太小会认不出人）。 */
const AVATAR_SIZE = { compact: 'sm', detail: 'md', comment: 'xs' } as const

/** 时间字号/位置跟着档位走。 */
const avatarSize = computed(() => AVATAR_SIZE[props.size])

/** 昵称是否真的能点：既要调用方允许，也要在路由上下文里（否则 RouterLink 会炸）。 */
const router = useRouter()
const canLink = computed(() => props.interactive && typeof router?.resolve === 'function')

/** 档案落点：本人 → `/me`（存在时），他人 → `/user/:id`。 */
const profileTo = computed(() => {
  if (props.self && router.hasRoute?.('me')) return { name: 'me' }
  return { name: 'user', params: { id: String(props.author.id) } }
})

/** 显示 handle 的条件：调用方没关、作者对象真有 username。 */
const handle = computed(() => {
  const explicit = props.showHandle
  const want = explicit === undefined ? props.size !== 'compact' : explicit
  if (!want) return ''
  const name = (props.author.username ?? '').trim()
  return name ? '@' + name : ''
})

/** 角色标签：admin 只是**标签**，不是授权（授权 100% 在服务端，铁律 I3）。 */
const roleLabel = computed(() => (props.author.role === 'admin' ? '管理员' : ''))
</script>

<template>
  <div class="author-row" :class="'author-row--' + props.size">
    <UserAvatar
      :user="{ id: props.author.id, displayName: props.author.displayName }"
      :size="avatarSize"
      :interactive="canLink"
      :self="props.self"
    />
    <div class="lines">
      <p class="line">
        <RouterLink v-if="canLink" class="name" :to="profileTo">{{ props.author.displayName }}</RouterLink>
        <span v-else class="name">{{ props.author.displayName }}</span>
        <span v-if="handle" class="handle dim">{{ handle }}</span>
        <span v-if="roleLabel" class="role">{{ roleLabel }}</span>
        <!-- 时间：compact / comment 与昵称同行；detail 由 CSS 折到下一行。 -->
        <RelativeTime v-if="props.createdAt" class="time dim" :value="props.createdAt" />
      </p>
    </div>
  </div>
</template>

<style scoped>
/*
 * 一行：头像 + 文本块。
 *
 * `align-items: center` 对 compact/comment 合适（头像与一行文字等高）；
 * detail 用 flex-start，因为那里昵称与时间会折成两行，居中对齐会让头像悬在中间。
 */
.author-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.author-row--detail {
  align-items: flex-start;
  gap: 10px;
}

.lines {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

/* 一行装下昵称 / handle / 角色 / 时间；窄屏折行而不是溢出。 */
.line {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 6px;
  margin: 0;
  min-width: 0;
}

/* detail 档把时间折到下一行：详情页里作者是主角，昵称值得独占一行。 */
.author-row--detail .line {
  gap: 8px;
}

.name {
  font-weight: 600;
  color: var(--text);
  overflow-wrap: anywhere;
  text-decoration: none;
}

/* 只有真的是链接时才给 hover 反馈（否则"看起来能点、点了没反应"更糟）。 */
a.name:hover {
  color: var(--accent);
  text-decoration: underline;
}

a.name:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}

.author-row--compact .name {
  font-size: 13px;
}

.author-row--detail .name {
  font-size: 15px;
}

.author-row--comment .name {
  font-size: 13px;
}

.handle {
  font-size: 12px;
  overflow-wrap: anywhere;
}

.role {
  padding: 0 5px;
  border: 1px solid var(--accent);
  color: var(--accent);
  font-size: 11px;
  line-height: 16px;
}

.time {
  font-size: 12px;
}

/* detail：时间折到第二行（`.lines` 是列，所以给 line 内部换成块级布局即可）。 */
.author-row--detail .time {
  flex-basis: 100%;
}
</style>
