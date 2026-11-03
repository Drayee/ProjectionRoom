<script setup lang="ts">
/**
 * 全局常驻顶栏（由 App.vue 挂载在路由出口之外，不属于任何页面）。
 *
 * 三种形态：
 *   - `app`    已登录应用态（品牌名 + 分区图标导航 + 头像/退出）
 *   - `page`   内容页（品牌名 + 右侧公开入口 / 登录注册）
 *   - `bare`   只有品牌名（邮件链接落点页：/reset /verify）
 *
 * 分区导航**只看登录态**，不看形态：旧实现把它绑在 shell 形态上，
 * 于是登录后从首页（shell）点到 /posts（page），导航整排消失。
 * 顶栏常驻 + 导航只受登录态驱动，换页入口才不会忽隐忽现。
 *
 * 归 ui/：读 stores/auth、用 RouterLink 与 useRouter 导航，不发请求（符合票 #11 的 ui/ 准入判据）。
 *
 * icon-first：导航只留图标 + aria-label + v-tip；文字只在主 CTA 上出现。
 */
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import BrandIcon from './BrandIcon.vue'
import UserAvatar from './UserAvatar.vue'
import IconButton from './IconButton.vue'
import { useAuthStore } from '../../stores/auth'
import { useFollowsStore } from '../../stores/follows'
import { prefetchRoute } from '../../utils/routePrefetch'
import { isSelfProfile, navSectionOf, type NavSection } from '../../utils/navSection'

const props = withDefaults(defineProps<{ variant?: 'app' | 'page' | 'bare' }>(), {
  variant: 'page',
})

const auth = useAuthStore()
const followsStore = useFollowsStore()
const router = useRouter()
const route = useRoute()

const busy = ref(false)

async function doLogout(): Promise<void> {
  if (busy.value) return
  busy.value = true
  try {
    await auth.logout()
    await router.push('/')
  } finally {
    busy.value = false
  }
}

/**
 * 顶栏头像的"人"（票 #61）。
 *
 * 顶栏此前用**名字哈希**取 `hsl(...)`：同一个人在顶栏是一个色、在好友页/帖子列表
 * 又是另一个色（那边走 `utils/avatar.ts` 的五色板）。两个色板 = 用户认不出人。
 * 票 #44 决议明确"顶栏那个 `hsl(hash)` 删除，统一走 `avatarOf()`"，
 * 并**接受这次视觉回归**（顶栏头像颜色会变）—— 这就是消除"同一用户两处不同色"的代价。
 *
 * 派生仍然只在 `utils/avatar.ts` 里做（唯一源）：这里只把 `id + displayName` 交给
 * `<UserAvatar>`，不自己算色。
 */
const avatarUser = computed(() => {
  const p = auth.profile
  // avatarUrl 是像素头像的**覆盖层**（票 #74）：有值才盖图，没有就是字母头。
  return { id: p?.id ?? 0, displayName: p?.displayName || p?.username || '?', avatarUrl: p?.avatarUrl ?? '' }
})

/** 登录/注册链接带上当前路径，登录后回到原处。 */
const redirectQuery = computed(() => ({ redirect: route.fullPath }))

/**
 * 分区导航表。`section` 是**归属键**，与 `utils/navSection.ts` 的 `NavSection` 同一批字符串。
 *
 * 这张表是"入口 → 分区"的正向映射；反向（"这个路径属于哪个分区"）在
 * `navSectionOf()` 的前缀表里。两处必须同时改 —— 新增一个导航项却漏改前缀表，
 * 症状是"点了能进，但那一项永远不亮"。
 */
const NAV: ReadonlyArray<{ name: string; icon: string; label: string; section: NavSection }> = [
  { name: 'public-rooms', icon: 'video', label: '公开房间', section: 'public-rooms' },
  { name: 'posts', icon: 'tips', label: '帖子', section: 'posts' },
  { name: 'forum', icon: 'chat', label: '番剧论坛', section: 'forum' },
  { name: 'friends', icon: 'users-group', label: '好友', section: 'friends' },
  { name: 'messages', icon: 'msg', label: '消息', section: 'messages' },
  { name: 'help', icon: 'help', label: '帮助', section: 'help' },
]

/**
 * 当前路径属于哪个分区（票 #77）。**唯一判定源**在 `utils/navSection.ts`：
 * 显式前缀表 + 本人优先，不依赖 vue-router 自带的 `router-link-active`
 * （那是按路由记录匹配，`/posts/3` 不会点亮 `/posts`，`/forum/7` 不会点亮 `/forum`）。
 */
const currentSection = computed(() => navSectionOf(route.path, auth.profile?.id ?? null))

/** 是否停在"我自己的档案页"（点亮头像位）。别人档案页不点亮任何东西。 */
const onSelfProfile = computed(() => isSelfProfile(route.path, auth.profile?.id ?? null))

/**
 * 导航项的当前态 = 该分区命中 且 **当前不在本人档案页**。
 *
 * 为什么后半个条件：`/user/1`（本人）按规则归"我"，不归任何分区；但 `/user/1`
 * 的路径**不**命中任何前缀，两者本来就互斥。这半个条件是为将来的 `/me`
 * 之类的"本人专属页"留的位置 —— 届时 `/me` 会命中前缀、同时也算"本人"，
 * 而"我"只该有一个高亮（头像位），不能整排也亮。
 */
function isActive(section: NavSection): boolean {
  return currentSection.value === section && !onSelfProfile.value
}

/**
 * 悬停预热（票 #79）：鼠标移到导航项上就把该页 chunk 取回来。
 *
 * 为什么是 pointerenter 而不是 mouseenter：pointerenter 在**触屏**上不触发，
 * 而 mouseenter 会在触屏模拟鼠标时误触发 —— 触屏用户不该因为"手指还没点下去"
 * 就下载一个页面。
 *
 * 失败静默由 `utils/routePrefetch.ts` 保证（catch 吞掉），这里不处理错误。
 */
function warmUp(name: string): void {
  void prefetchRoute(name)
}

/**
 * 「消息」入口上的未读角标（wayfinder #30）。
 *
 * 只读 `auth.unread` 这一个来源（`stores/auth.ts` 的 `setUnread` 是唯一写入点）：
 * 顶栏角标、消息页的会话徽标、"读完清零"必须是同一个数字，各存一份必然出现
 * "顶栏 3 条、页面 0 条"。
 *
 * `null`（服务端没返回未读数）时**不显示**角标 —— 显示 0 等于替服务端断言
 * "你没有未读"，而那正是 #30 要避免的谎言。超过 99 显示 `99+`：
 * 三位数会把 36px 的图标位撑变形。
 */
const unreadLabel = computed(() => {
  const n = auth.unread
  if (n === null || n <= 0) return ''
  return n > 99 ? '99+' : String(n)
})

/**
 * 「好友」入口上的粉丝红点（票 #67；决议 #53 §1、§3）。
 *
 * **另一个数、另一个来源**：`stores/follows.ts` 的 `newFollowers`，
 * 与上面的 `unreadLabel` 毫无关系。决议 §3 把这条写得最重 ——
 * 混用一个数字的症状是"读了私信、粉丝红点也灭了"。
 *
 * 两个角标在同一个导航栏里并排出现（消息 + 好友）是**有意**的：
 * 它们回答两个不同的问题（"有人给我发消息" / "有人新关注了我"），
 * 合成一个数字会让人以为"点进去就一起清"。
 */
const followLabel = computed(() => followsStore.badge)

/**
 * 一个导航项该显示哪个角标。
 *
 * 返回空串 = 不显示。写成函数而不是在模板里塞两个 `v-if`：
 * 将来加第三个角标（比如"待处理举报"）时，改的是这一处。
 */
function badgeOf(name: string): string {
  if (name === 'messages') return unreadLabel.value
  if (name === 'friends') return followLabel.value
  return ''
}

/** 角标的无障碍说明（读屏要知道这个数字是什么）。 */
function badgeHint(name: string): string {
  if (name === 'messages') return `消息（${unreadLabel.value} 条未读）`
  if (name === 'friends') return `好友（${followLabel.value} 个新粉丝）`
  return ''
}
</script>

<template>
  <header class="hdr" :class="`hdr--${props.variant}`">
    <RouterLink class="brand" to="/" aria-label="月喵（返回首页）" data-testid="auth-brand">
      <span class="brand-text">月喵</span>
    </RouterLink>

    <!-- 分区导航：只看登录态，与路由变体无关。
         旧实现按 variant==='shell' 才显示，于是登录后从首页（shell）点到
         /posts（page）导航整排消失。顶栏常驻后，导航必须只受登录态驱动。 -->
    <nav v-if="auth.isLoggedIn && props.variant !== 'bare'" class="nav" aria-label="分区导航">
      <RouterLink
        v-for="item in NAV"
        :key="item.name"
        class="nav-btn"
        :class="{ 'is-active': isActive(item.section) }"
        :to="{ name: item.name }"
        :aria-current="isActive(item.section) ? 'page' : undefined"
        :aria-label="badgeOf(item.name) ? badgeHint(item.name) : item.label"
        v-tip="item.label"
        @pointerenter="warmUp(item.name)"
      >
        <BrandIcon :name="item.icon" decorative :size="18" />
        <!-- 角标：只在有数字时出现（`null` 与 0 都不显示，理由见 unreadLabel / followLabel）。
             消息读未读数、好友读粉丝红点 —— 两个不同的数，两个不同的来源。 -->
        <span
          v-if="badgeOf(item.name)"
          class="dot"
          :data-testid="item.name === 'friends' ? 'follow-red-dot' : undefined"
          aria-hidden="true"
        >
          {{ badgeOf(item.name) }}
        </span>
      </RouterLink>
    </nav>

    <div class="right">
      <!-- 未登录：右侧给两个公开入口（公开房间 / 帮助）。
           已登录：分区导航里已有这两项，右侧只放账号相关，避免重复。 -->
      <template v-if="!auth.isLoggedIn">
        <RouterLink
          v-if="props.variant !== 'bare'"
          class="nav-btn"
          :class="{ 'is-active': isActive('public-rooms') }"
          :to="{ name: 'public-rooms' }"
          :aria-current="isActive('public-rooms') ? 'page' : undefined"
          aria-label="公开房间"
          v-tip="'公开房间'"
          @pointerenter="warmUp('public-rooms')"
        >
          <BrandIcon name="video" decorative :size="17" />
        </RouterLink>
        <RouterLink
          v-if="props.variant !== 'bare'"
          class="nav-btn"
          :class="{ 'is-active': isActive('help') }"
          :to="{ name: 'help' }"
          :aria-current="isActive('help') ? 'page' : undefined"
          aria-label="帮助"
          v-tip="'帮助'"
          @pointerenter="warmUp('help')"
        >
          <BrandIcon name="help" decorative :size="17" />
        </RouterLink>
      </template>

      <template v-if="props.variant !== 'bare'">
        <template v-if="auth.isLoggedIn">
          <RouterLink
            v-if="auth.profile?.role === 'admin'"
            class="nav-btn"
            :class="{ 'is-active': isActive('admin') }"
            :to="{ name: 'admin' }"
            :aria-current="isActive('admin') ? 'page' : undefined"
            aria-label="管理端"
            v-tip="'管理端'"
            @pointerenter="warmUp('admin')"
          >
            <BrandIcon name="settings" decorative :size="17" />
          </RouterLink>
          <!-- 头像 = 导航（票 #44：头像位不加编辑态，避免进主页/改图二义）。
               本人 → /me（票 #69 落地后自动生效），他人 → /user/:id。
               组件内部自问路由表"有 me 吗"，所以这里不用传额外开关。

               当前态（票 #77）：停在**本人**档案页时点亮头像位 —— 按规则
               `/user/<我>` 不归任何分区，所以这时整排分区图标全灭、只有头像亮，
               用户一眼知道"这是我自己那页"。别人档案页则全灭（那里没有"我的"语义）。 -->
          <span class="avatar-slot" :class="{ 'is-active': onSelfProfile }">
            <UserAvatar :user="avatarUser" :self="true" size="md" />
          </span>
          <!-- 票 #62：顶栏的裸 button 换成 ui/IconButton。
               它的五态（提亮 / 按压缩放 / 键盘外环 / 禁用降饱和）与按钮令牌是全站唯一出口，
               loading 等价于"退出中"：锁宽不跳版，且不挡重复点击之外的任何东西。 -->
          <IconButton
            class="nav-btn nav-btn--ui"
            icon="logout"
            label="退出登录"
            :loading="busy"
            v-tip="'退出登录'"
            @click="doLogout"
          />
        </template>
        <template v-else>
          <RouterLink class="nav-btn" :to="{ name: 'login', query: redirectQuery }" aria-label="登录" v-tip="'登录'">
            <BrandIcon name="login" decorative :size="17" />
          </RouterLink>
          <RouterLink
            class="nav-btn"
            :to="{ name: 'register', query: redirectQuery }"
            aria-label="注册"
            v-tip="'注册'"
          >
            <BrandIcon name="users-group" decorative :size="17" />
          </RouterLink>
        </template>
      </template>
    </div>
  </header>
</template>

<style scoped>
.hdr {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 18px;
  border-bottom: 1px solid var(--line);
  overflow: hidden;
}

.brand {
  display: flex;
  align-items: center;
  text-decoration: none;
  color: var(--text);
}

.brand:hover {
  text-decoration: none;
}

/* 品牌名用故障黑体：B 的字形本身承担故障美学 */
.brand-text {
  font-family: var(--font-glitch);
  font-size: 24px;
  letter-spacing: 0.04em;
}

/* 顶栏滑过的故障线（常驻；降级时由 tokens.css 判死） */
.hdr::after {
  content: '';
  position: absolute;
  left: 0;
  bottom: 0;
  width: 100%;
  height: 2px;
  background: linear-gradient(90deg, transparent, var(--accent), var(--glitch-r), transparent);
  animation: barline var(--glitch-barline) linear infinite;
}

@keyframes barline {
  from {
    transform: translateX(-100%);
  }
  to {
    transform: translateX(100%);
  }
}

.nav {
  display: flex;
  align-items: center;
  gap: 2px;
  margin-left: 6px;
}

.right {
  display: flex;
  align-items: center;
  gap: 2px;
  margin-left: auto;
}

.nav-btn {
  /* 状态色与图标位统一到这里（票 #62）：链接形态与 ui/IconButton 形态读同一组变量，
     于是 hover 提亮 / 按下 用的是 tokens.css 里那两个绝对色，而不是各写一份 rgba。 */
  --ui-btn-bg-hover: var(--state-hover);
  --ui-btn-bg-active: var(--state-active);
  --ui-btn-fg-hover: var(--text);
  --ui-btn-fg-active: var(--text);

  display: inline-flex;
  align-items: center;
  justify-content: center;
  /* 未读角标用绝对定位挂在右上角（见 .dot），所以要有个定位上下文。 */
  position: relative;
  width: 36px;
  height: 36px;
  color: var(--dim);
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
  text-decoration: none;
  transition:
    color var(--dur-micro) var(--ease),
    border-color var(--dur-micro) var(--ease),
    background var(--dur-micro) var(--ease);
}

/* 未读角标：极小、贴在图标右上角。`pointer-events: none` 让它不吃点击 ——
   点角标的本意一定是"打开消息"，不是"点这个角标"。 */
.dot {
  position: absolute;
  top: 1px;
  right: 1px;
  min-width: 15px;
  height: 15px;
  padding: 0 4px;
  border-radius: 8px;
  background: var(--accent);
  color: var(--accent-ink);
  font-size: 10px;
  font-weight: 700;
  line-height: 15px;
  text-align: center;
  pointer-events: none;
}

/* 悬停：票 #43 §2 三件套的顶栏版 —— ① 底 --state-hover ② 前景升 --text
   ③ 无位移（图标按钮档）。旧实现手写 rgba(180,108,255,.08)，与状态令牌不同源。

   颜色走 `.nav-btn` 顶部声明的那组 `--ui-btn-*`（票 #62）：链接形态与 ui/IconButton
   形态读同一组变量，两种形态的 hover 才是同一个色，而不是各写一份。
   底色本身仍是 `--state-hover`（票 #77），只是经由变量间接读到。 */
.nav-btn:hover {
  color: var(--ui-btn-fg-hover);
  background: var(--ui-btn-bg-hover);
  border-color: var(--line);
  text-decoration: none;
}

/* 按压：底色换 --state-active + 按图标档缩放（票 #43 §4：36px 用 --press-icon）。
   旧值 .92 与原型不一致，这里统一到令牌。 */
.nav-btn:active {
  background: var(--ui-btn-bg-active);
  transform: scale(var(--press-icon));
}

/* 命中区 44×44（WCAG 2.5.5 AAA，票 #43 §6）：视觉仍是 36px，伪元素向外扩 4px。
   用 position:absolute 而不是负 margin：负 margin 会改变布局盒，把这一排间距挤歪。
   （ui/IconButton 形态自带 `::after` 命中区，两者叠在同一位置，互不影响。） */
.nav-btn::before {
  content: '';
  position: absolute;
  inset: -4px;
}

/* 按钮形态的导航位（票 #62）：ui/IconButton 自带尺寸与状态样式，
   这里只把它接回顶栏那套 36px 图标位的描边 hover。状态色不在这里重写。 */
.nav-btn--ui:not(:disabled):hover {
  border-color: var(--line);
}

/**
 * 当前态（票 #77；外观规范见票 #43 §5）。
 *
 *   形态   静态 pill：background 用 --state-active（比 hover 更亮，两级分得开）
 *   前景   图标从 --dim 升到 --text
 *   分离   1px 静态双色：右 +1px 红、左 -1px 紫（与 #13 按钮「右红左紫」同向）
 *   动画   零新增 —— 切换时**没有**滑动指示器；底色走既有的 --dur-micro 过渡，
 *          reduced-motion 下由 tokens.css 全局判死，属性值仍然变化（不是靠动画到终态）。
 *
 * 为什么要加边框：双色分离靠 text-shadow 表达，而图标是 SVG（BrandIcon），
 * text-shadow 作用不到它。给按钮本身一条 1px 的实线边框（透明 → accent），
 * 让"当前态"在图标上也有一个**非颜色**的形态标记（描边本身不承担语义，
 * 语义由 aria-current 与底色承担，符合票 #13 硬约束 2）。
 */
.nav-btn.is-active {
  color: var(--text);
  background: var(--state-active);
  border-color: var(--accent);
}

/* 双色分离：右红左紫。加在图标上（:deep 穿透 BrandIcon 的 svg 本体），
   而不是按钮的文本 —— 这些按钮里没有文本节点。 */
.nav-btn.is-active :deep(.brand-icon) {
  filter: none;
  text-shadow:
    1px 0 0 var(--glitch-r),
    -1px 0 0 var(--accent);
}

/* 当前态下悬停：保持已激活的底色，不再回退到 hover 色（否则"当前"看起来像"路过"）。 */
.nav-btn.is-active:hover {
  background: var(--state-active);
}

/* 头像位（票 #77）：本人档案页时，头像外面套一圈与 .nav-btn 同款的静态 pill。
   用 span 包一层而不是改 UserAvatar —— 头像组件的形状是圆的、契约在 #61 里钉死，
   不该为了"导航高亮"再长出第二个形态。 */
.avatar-slot {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  position: relative;
  padding: 2px;
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
}

.avatar-slot.is-active {
  background: var(--state-active);
  border-color: var(--accent);
}

/* 图标悬停抖动：故障语言（位置不稳），阶梯而非平滑 */
.nav-btn:hover :deep(.brand-icon) {
  animation: icon-jit var(--glitch-icon-jit) steps(2, end);
}

@keyframes icon-jit {
  0% {
    transform: translateX(0);
  }
  33% {
    transform: translateX(-2px);
  }
  66% {
    transform: translateX(2px);
  }
  100% {
    transform: translateX(0);
  }
}

/* bare 形态：只有品牌名，居中偏左 */
.hdr--bare .right {
  display: none;
}
</style>
