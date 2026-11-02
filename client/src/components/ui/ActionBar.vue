<script setup lang="ts">
/**
 * ActionBar —— 一条内容上「能做什么 + 有多少」的唯一出口（票 #63，契约见决议 #44）。
 *
 * ## 为什么它必须与卡片链接**分开**
 *
 * 帖子卡原先整块是一个 `<RouterLink>`，于是"点卡片进详情"与"点卡片里的赞"是同一个
 * 点击区域 —— 在链接里放按钮是**无效 HTML**（浏览器会把内层按钮从可点区域里摘掉，
 * 或者点按钮时同时触发导航）。真机脚本 `verify-authorrow.mjs` 的 R2c 直接钉住这条：
 * `a.card-link [data-testid=action-bar]` 与 `a.card-link button` 的数量必须都是 0。
 *
 * 所以卡片被拆成「链接区（标题+摘要，整块可点）」+「动作条（独立的一组按钮）」。
 *
 * ## 顺序是契约
 *
 * 赞 → 收藏 → 评论（DOM 顺序 = 视觉顺序 = 键盘 Tab 顺序）。真机脚本 R3/R7 按这个顺序判，
 * 改顺序会让"Tab 一次就能到赞"这个可用性结论失效。
 *
 * ## 计数
 *
 * 每个按钮都有 `.count` 节点（哪怕隐藏），文案走 `utils/count.ts` 的 `formatCount`：
 * 0 时整个数字隐藏（`data-hidden`）而不是印一个「0」——「赞 0」比「赞」更像坏数据。
 *
 * ## 收藏是**占位**
 *
 * 票 #63 只做形态（按钮在、有 `aria-pressed`、有可访问名），数据接线在票 #64（多态收藏）。
 * 所以 `favorited` 默认 false、点击只发事件不落库 —— 这里刻意不假装它能用。
 *
 * ## 归 ui/
 *
 * 纯展示：不做请求、不读 store。乐观更新与回滚在**调用方**（它才知道失败要退回什么值），
 * 本组件只负责把"按下去"这件事说清楚（`pressed` / `busy` / `disabled`）。
 */
import BrandIcon from './BrandIcon.vue'
import { formatCount } from '../../utils/count'

const props = withDefaults(
  defineProps<{
    /** 当前用户是否已赞（决定 `aria-pressed` 与视觉强调）。 */
    liked?: boolean
    /** 赞的计数（0 时不显示数字）。 */
    likeCount?: number
    /** 是否已收藏（票 #64 接线前恒为 false）。 */
    favorited?: boolean
    /** 收藏的计数（0 时不显示数字）。 */
    favoriteCount?: number
    /** 评论数（0 时不显示数字）。 */
    commentCount?: number
    /** 点赞在途：按钮显示 spinner，且**挡住重复触发**（单条在途锁的视觉面）。 */
    likeBusy?: boolean
    /** 收藏在途。 */
    favoriteBusy?: boolean
    /**
     * 是否显示评论按钮。
     *
     * 详情页自己就是评论区，那里不需要"跳到评论"—— 调用方传 false 把它摘掉。
     */
    showComment?: boolean
  }>(),
  {
    liked: false,
    likeCount: 0,
    favorited: false,
    favoriteCount: 0,
    commentCount: 0,
    likeBusy: false,
    favoriteBusy: false,
    showComment: true,
  },
)

defineEmits<{ like: []; favorite: []; comment: [] }>()

/**
 * 计数文案（空串 = 不显示）。
 *
 * 为什么在这里算而不是在模板里写 `formatCount(x) || '0'`：0 的语义是**隐藏**，
 * 而模板里那个 `||` 会把它变成「0」——正是本组件要消灭的形态。
 */
function countText(n: number): string {
  return formatCount(n)
}
</script>

<template>
  <div class="action-bar" data-testid="action-bar">
    <!-- 赞：可点，乐观更新由调用方做（这里只反映"在途"）。 -->
    <button
      type="button"
      class="act"
      data-action="like"
      :class="{ 'is-on': props.liked, 'is-busy': props.likeBusy }"
      :aria-pressed="props.liked"
      :aria-label="props.liked ? '取消点赞' : '点赞'"
      :aria-busy="props.likeBusy || undefined"
      @click="$emit('like')"
    >
      <BrandIcon :name="props.liked ? 'heart-fill' : 'heart'" decorative :size="14" />
      <span class="label">赞</span>
      <span class="count" :data-hidden="countText(props.likeCount) === '' ? '' : undefined">
        {{ countText(props.likeCount) }}
      </span>
    </button>

    <!-- 收藏：本票占位（票 #64 接线）。aria-pressed 现在就给，接线后不用改契约。 -->
    <button
      type="button"
      class="act"
      data-action="favorite"
      :class="{ 'is-on': props.favorited, 'is-busy': props.favoriteBusy }"
      :aria-pressed="props.favorited"
      :aria-label="props.favorited ? '取消收藏' : '收藏'"
      :aria-busy="props.favoriteBusy || undefined"
      @click="$emit('favorite')"
    >
      <BrandIcon :name="props.favorited ? 'star-fill' : 'star'" decorative :size="14" />
      <span class="label">收藏</span>
      <span class="count" :data-hidden="countText(props.favoriteCount) === '' ? '' : undefined">
        {{ countText(props.favoriteCount) }}
      </span>
    </button>

    <button
      v-if="props.showComment"
      type="button"
      class="act"
      data-action="comment"
      aria-label="查看评论"
      @click="$emit('comment')"
    >
      <BrandIcon name="chat" decorative :size="14" />
      <span class="label">评论</span>
      <span class="count" :data-hidden="countText(props.commentCount) === '' ? '' : undefined">
        {{ countText(props.commentCount) }}
      </span>
    </button>
  </div>
</template>

<style scoped>
.action-bar {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}

/*
 * 单个动作。
 *
 * 命中区按票 #43 §6：高度 ≥32px、左右 padding ≥8px。视觉上按钮很轻（无边框、dim 字色），
 * 但热区不小 —— 「视觉小、热区大」是本仓一致的做法（顶栏 nav-btn 的 ::before 同理）。
 */
.act {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-height: 32px;
  padding: 0 8px;
  border: 1px solid transparent;
  border-radius: var(--radius);
  background: transparent;
  color: var(--dim);
  font-size: 12px;
  line-height: 1.5;
  cursor: pointer;
  transition:
    background-color var(--dur-micro) var(--ease),
    color var(--dur-micro) var(--ease),
    border-color var(--dur-micro) var(--ease);
}

.act:hover {
  background: var(--state-hover);
  color: var(--text);
}

.act:active {
  background: var(--state-active);
}

.act:focus-visible {
  outline: var(--ring);
  outline-offset: var(--ring-offset);
}

/* 已赞/已收藏：颜色 + 描边一起变（状态不靠单一通道表达）。 */
.act.is-on {
  color: var(--glitch-p);
  border-color: var(--glitch-p);
}

.act.is-on:hover {
  background: var(--state-hover);
  color: var(--glitch-p);
}

/* 在途：降一点透明度表示"正在处理"，但**不 disabled**（仍可聚焦，见 Button 的同一约定）。 */
.act.is-busy {
  opacity: 0.6;
}

.label {
  /* 窄卡片上标签可以省略，只留图标与计数 —— 由容器查询/断点决定，不在这里写死。 */
  white-space: nowrap;
}

.count {
  font-variant-numeric: tabular-nums;
}

/* 计数为 0：整块隐藏（不是印 0）。用属性选择器而不是 v-if，
   这样真机脚本能读到 `data-hidden` 这个**结构事实**，而不是靠"节点不存在"去猜。 */
.count[data-hidden] {
  display: none;
}

/* 降级：颜色与透明度仍在变（那是状态反馈），只把位移/缩放类动效关掉。 */
@media (prefers-reduced-motion: reduce) {
  .act {
    transition:
      background-color var(--dur-micro) var(--ease),
      color var(--dur-micro) var(--ease),
      border-color var(--dur-micro) var(--ease);
  }
}
</style>
