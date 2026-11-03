<script setup lang="ts">
/**
 * UserAvatar —— 全站唯一的头像组件（票 #61，契约见 #44 决议 + GLOSSARY「Shared object components」）。
 *
 * ## 它是什么
 * 一个「首字母 + 稳定色」的圆形块，上面可以**盖一张像素头像**（票 #74 / 决议 #54 §4）。
 * 像素头像只是**可选覆盖**，字母头永远兜底 —— 这是本组件最重要的性质：
 * 任何调用方都不需要判断"有没有头像"，也不需要处理加载失败。
 *
 * ## 为什么必须有这个组件（不是"再写一个 span"）
 * 站内的头像字母块散在五处，每处自己算色：
 *   - `AppHeader.vue` 用**名字哈希**取 `hsl(...)`（顶栏一个人一个色，与别处不同色）；
 *   - `FriendsPage` / `MessagesPage` / `AnimeBoardPage` 用 `utils/avatar.ts` 的五色板；
 *   - `UserPage` 又自己 `avatarOf(...)` 了一次。
 * 结果是"同一个用户，顶栏一个色、帖子列表另一个色、好友页第三个色" —— 用户认不出人。
 * 票 #44 决议明确：**顶栏那个 `hsl(hash)` 取色删除，统一走 `avatarOf()` 五色板**，
 * 并接受这次视觉回归（顶栏头像颜色会变）。这就是"消除同一用户两处不同色"的代价。
 *
 * ## 唯一源与禁止复制
 * 字母与颜色只由 `client/src/utils/avatar.ts` 的 `avatarOf()` 产生（GLOSSARY：
 * 「页面里再写私有 `avatarOf` 视为回归」）。本组件**不含任何取色逻辑**，
 * 只负责把 `avatarOf()` 的结果渲染成可点/不可点的圆块。
 *
 * ## 三态回落（票 #74，决议 #54 §4）
 * 像素头像有三种"没有"的情形，**一律**回落到字母头：
 *   1. `avatarUrl` 为空 —— 从未设置、被管理员重置、或账号已注销（服务端置空）；
 *   2. 图片加载失败（对象被删、网络抖动、CSP 拦截）；
 *   3. 加载失败**之后**，本次会话内不再重试这个 URL。
 *
 * 第 3 条是必须的：列表页有几十个头像，其中一个 404 时浏览器会**每次重渲染都重试**
 * —— 症状是列表里那一格反复闪一下破图。记进模块级集合后它只失败一次。
 *
 * 覆盖层本身（`<img>` + `object-fit: cover` + 失败记忆）在 `ui/AvatarImage.vue`，
 * 失败集合在 `utils/avatarImage.ts` —— `/me` 与 `/user/:id` 的两处大头像
 * 用的是同一个覆盖层组件，所以那条纪律只有一份实现。
 *
 * ## 分层（票 #44 §0）
 * `ui/` = 纯展示：不读 store、不 import 路由模块，props 进 / 事件出。
 * 所以「这是不是我本人」这件事**由调用方判断**（传 `self`），组件自己不读登录态。
 *
 * ## 无障碍
 * - 装饰性（`interactive=false`）：`aria-hidden="true"`，读屏跳过。
 * - 可交互：整体是 `<RouterLink>`，`aria-label="查看 <displayName> 的档案"`。
 * - 像素头像是**装饰**（`alt=""` + `aria-hidden`）：名字已经由 aria-label 给出，
 *   再读一遍文件名或"头像"只会制造噪音。
 * - hover / focus-visible 有可感知反馈（与 Button 的 hover 语言同款）。
 *
 * ## 尺寸
 * `xs` 20px / `sm` 26px / `md` 32px / `lg` 40px。字号随尺寸等比缩放，
 * 保证首字母在圆里始终居中不溢出（中文名一个汉字、英文名一个字母都要能放下）。
 */
import { computed } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import AvatarImage from './AvatarImage.vue'
import { avatarOf } from '../../utils/avatar'

/** 尺寸档位（#44 决议：xs | sm | md | lg）。 */
export type AvatarSize = 'xs' | 'sm' | 'md' | 'lg'

const props = withDefaults(
  defineProps<{
    /**
     * 人：id + 昵称（决定字母与色板），可选 `avatarUrl`（像素头像覆盖层）。
     *
     * `avatarUrl` 刻意是可选的：调用方拿到的数据里可能没有这个字段
     *（旧接口、mock 数据、注销账号）。缺了就是字母头 —— 不需要调用方补默认值。
     */
    user: { id: number; displayName: string; avatarUrl?: string | null }
    /** 尺寸档位。 */
    size?: AvatarSize
    /**
     * 是否可点（默认 true）。
     *
     * **嵌套链接处必须传 false**（私信气泡头、论坛主题行 —— 外层已经是 `<RouterLink>`）：
     * 链接套链接是无效 HTML，浏览器会把内层链接从可点区域里摘掉，
     * 表现是"点头像没反应"—— 那种静默缺陷最难查。
     * 另有一处是**嵌在 `<button>` 里**（消息会话行）：按钮里也不能放链接，
     * 同样传 false。
     */
    interactive?: boolean
    /**
     * 这是不是**当前登录用户本人**。
     *
     * 本人点头像进 `/me`（自己的管理台），他人进 `/user/:id`（公开档案）。
     * 调用方判断（不读 store —— `ui/` 是纯展示层）。
     */
    self?: boolean
  }>(),
  { size: 'md', interactive: true, self: false },
)

/**
 * 路由实例（注入拿，不是 import）。
 *
 * 为什么需要它：本人头像的落点是 `/me`，而 `/me` 属票 #69，**现在还不存在**。
 * 链接到一个不存在的路由 = 点下去掉进空白页（vue-router 只会 warn，不会报错）——
 * 那是比"进自己的公开档案"差得多的结局。
 *
 * 所以这里问路由表一句"有 `me` 吗"：有就进管理台，没有就退回本人公开档案。
 * 等票 #69 落地、`/me` 一进路由表，这个判断自动生效，**不用改任何调用方**。
 *
 * 用 `useRouter()`（注入）而不是 `import { router } from '../../router'`：
 * 后者会让 `ui/` 变成有模块级依赖（而 `router.ts` 又 import 全部页面 → 成环）。
 * 注入没有这个问题：`RouterLink` 本身也依赖 router 注入，本组件不可能脱离 router 上下文渲染。
 */
const router = useRouter()

/** `/me` 是否已经存在（票 #69 落地后为 true）。 */
const hasMeRoute = computed(() => {
  const r = router as unknown as { hasRoute?: (name: string) => boolean }
  if (typeof r?.hasRoute !== 'function') return false
  return r.hasRoute('me') || r.hasRoute('/me')
})

/** 派生只走唯一源（utils/avatar.ts）。组件里不许再算一次色。 */
const derived = computed(() => avatarOf(props.user.id, props.user.displayName))

/** 尺寸 → 像素。数值写死在这里而不是 CSS 变量：四档是契约的一部分，别处要按它排版。 */
const SIZE_PX: Record<AvatarSize, number> = { xs: 20, sm: 26, md: 32, lg: 40 }

/** 字号按尺寸等比走（圆里放一个汉字/字母，太多会溢出）。 */
const FONT_PX: Record<AvatarSize, number> = { xs: 11, sm: 12, md: 14, lg: 18 }

const sizePx = computed(() => SIZE_PX[props.size])
const fontPx = computed(() => FONT_PX[props.size])

/**
 * 圆块的样式。
 *
 * 底色**始终**是字母头的颜色（哪怕有像素头像）：图片还没解码完的那一瞬间
 * 与图片失败之后，用户看到的都应该是那个有辨识度的色块，而不是一片空白。
 */
const boxStyle = computed(() => ({
  background: derived.value.avatarColor,
  width: sizePx.value + 'px',
  height: sizePx.value + 'px',
  fontSize: fontPx.value + 'px',
}))

/** 无障碍名：可点时告诉读屏"这是谁的档案"。 */
const label = computed(() => `查看 ${props.user.displayName || '用户'} 的档案`)

/**
 * 落点：本人 → `/me`（管理台，且该路由已存在时）；他人 → `/user/:id`。
 *
 * 公开档案**免登录可读**（服务端 `GET /api/users/:id` 不要求登录），
 * 所以未登录也可以点 —— 这里不做任何登录态判断。
 */
const target = computed(() => {
  if (props.self && hasMeRoute.value) return { name: 'me' }
  return { name: 'user', params: { id: String(props.user.id) } }
})

/**
 * 共享元素契约（票 #78）：`data-shared="avatar:<userId>"`。
 *
 * 对端是档案页（`/user/:id` 与 `/me`）名片区那枚大头像 —— 两端都写同一个值，
 * 匹配**只按这个值**，不写死"列表→档案"的页对：从 `/me` 的帖子块、从论坛主题行、
 * 从私信会话点头像，走的都是同一条规则。
 *
 * 为什么给每个头像都带（包括 `interactive=false` 的装饰性头像）：装饰性头像自己点不动，
 * 但它所在的那一行可能是链接 —— 值得让"点这一行进档案页"也接上同一个元素。
 * 至于会不会真的飞，由决策器与两端存在性说了算（见 `utils/sharedElement.ts`）。
 */
const sharedId = computed(() => `avatar:${props.user.id}`)
</script>

<template>
  <RouterLink
    v-if="props.interactive"
    class="avatar"
    :to="target"
    :aria-label="label"
    :style="boxStyle"
    :data-shared="sharedId"
  >
    <!-- 字母头先渲染（它是兜底），像素头像绝对定位盖在上面。
         顺序有讲究：反过来（先图后字母）会在图片解码前露出色块再被图盖住，
         那一下闪烁在长列表里非常刺眼。 -->
    <span class="char" aria-hidden="true">{{ derived.avatarChar }}</span>
    <AvatarImage :url="props.user.avatarUrl" />
  </RouterLink>
  <span v-else class="avatar" aria-hidden="true" :style="boxStyle" :data-shared="sharedId">
    <span class="char" aria-hidden="true">{{ derived.avatarChar }}</span>
    <AvatarImage :url="props.user.avatarUrl" />
  </span>
</template>

<style scoped>
/**
 * 圆块本体。
 *
 * 颜色来自 `avatarOf()`（内联 background），这里只管形状与排版 —— 组件里
 * **不写状态色**（票 #43 的状态令牌纪律：禁手写 `rgba(...)` 状态色）。
 *
 * `position: relative` 是为了让像素头像能绝对定位盖住字母（见 .pic）。
 */
.avatar {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  border-radius: 50%;
  color: #fff;
  font-weight: 700;
  line-height: 1;
  text-decoration: none;
  overflow: hidden;
  transition:
    filter var(--dur-micro) var(--ease),
    transform var(--dur-micro) var(--ease);
}

/* hover：亮度提亮 + 轻微放大 —— 与 Button 的 hover 语言同款（底色/前景/位移三件套）。
   位移幅度按尺寸分级：越小的圆块位移越小，免得小圆块在列表里晃。 */
.avatar:hover {
  filter: brightness(1.12);
  transform: scale(1.06);
}

/* 键盘焦点：统一外环（票 #43：--ring + --ring-offset）。
   令牌若尚未落地（票 #62 才引入状态令牌），这里退回 accent，
   保证焦点环**现在就有**而不是等令牌上线。 */
.avatar:focus-visible {
  outline: 2px solid var(--ring, var(--accent));
  outline-offset: 2px;
}

.char {
  display: block;
  transform: translateY(0.02em);
}

/* 像素头像覆盖层的样式在 `ui/AvatarImage.vue`（父级只需 position: relative +
   overflow: hidden，本组件已有）。 */

/**
 * 降级：`prefers-reduced-motion: reduce` 下取消位移与缩放，只留颜色变化。
 * （tokens.css 已有全局判死；这里是本组件自己的位移类动效的显式降级。）
 */
@media (prefers-reduced-motion: reduce) {
  .avatar {
    transition: filter var(--dur-micro) var(--ease);
  }

  .avatar:hover {
    transform: none;
  }
}
</style>
