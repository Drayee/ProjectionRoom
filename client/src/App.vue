<script setup lang="ts">
/**
 * 全局外壳：顶栏常驻 + 路由出口。
 *
 * 顶栏**不属于任何页面**。旧架构里每个页面自己渲染 AppHeader，
 * 于是换页时顶栏被销毁重建：① 故障线动画每次跳转都重启；
 * ② 更致命的是各页 variant 不一致（首页登录态 shell 带分区导航，
 *    跳到 /posts 却是 page 无导航）——点一下导航，整排入口就"消失"了。
 *
 * 这里把顶栏提到路由出口**之外**，形态由路由元信息唯一决定：
 * 换页只替换 <router-view>，顶栏组件实例保持不变，导航不会闪、不会变形。
 *
 * 落地页（/）刻意无顶栏：它是全屏 hero，顶栏会抢首屏焦点。
 * 登录/注册页（/login /register）也不走全局顶栏：它们复用 LandingView 的
 * 展开态，自带窗口级关闭按钮，由 LandingView 自己决定要不要顶栏。
 *
 * 授权 100% 在服务端（铁律 I3）：这里只按路由决定"显示什么"。
 */
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import AppHeader from './components/ui/AppHeader.vue'
import RouteSkeleton from './components/ui/RouteSkeleton.vue'
import TooltipLayer from './components/ui/TooltipLayer.vue'
import ToastLayer from './components/ui/ToastLayer.vue'
import { announcement } from './utils/pageAnnouncer'
import { routePending } from './utils/routeLoadState'
import { useAuthStore } from './stores/auth'
import { useRouteTransition } from './composables/useRouteTransition'
import { useUserChannel } from './composables/useUserChannel'

type HeaderVariant = 'app' | 'page' | 'bare'

const route = useRoute()
const auth = useAuthStore()

/**
 * 用户级实时通道（票 #67）。
 *
 * 挂在外壳上而不是某个页面里：连接的生命周期该跟**登录态**走，而不是跟
 * "当前在哪个页面"走 —— 挂在页面里会导致换页时断开重连，而重连要重新取票
 * （票是单次使用的），换页频繁时那段空窗就再也收不到提示。
 *
 * 它只在登录时连（见 composable 里的 watch），未登录时什么都不做。
 */
useUserChannel()

/**
 * 顶栏形态：路由 meta.header 声明；缺省 'page'。
 *   app    已登录应用态（品牌名 + 分区图标导航 + 头像）
 *   page   内容页（品牌名 + 右侧公开入口 / 登录注册）
 *   bare   只有品牌名（邮件链接落点页）
 *   none   不渲染顶栏（落地页、登录/注册壳）
 *
 * 例外（首页）：路由写 none，因为未登录时是全屏落地页（顶栏会抢首屏焦点）；
 * 但**已登录**时要换成 app 态——否则登录后分区导航整排消失。
 */
const variant = computed<HeaderVariant | null>(() => {
  const v = route.meta.header
  if (v === 'none') {
    return auth.isLoggedIn && route.name === 'home' ? 'app' : null
  }
  if (v === 'app' || v === 'page' || v === 'bare') return v
  return 'page'
})

/** 落地页与登录/注册壳自带全屏布局，不套全局 padding。 */
const chromeless = computed(() => {
  const n = route.name
  return n === 'home' || n === 'login' || n === 'register'
})

/**
 * 懒加载途中的骨架（票 #79）。
 *
 * 只在**换页等待**时出现（routePending > 0），首屏不出现。
 * 注意它与旧页是**并存**关系：骨架盖在路由出口上方，旧页留在下面 ——
 * 这样等 chunk 时画面不是空白，也不会出现"内容闪一下再换"。
 *
 * 与票 #76 的过渡壳**叠加**而非二选一：过渡决定"两页怎么交替"，
 * 骨架决定"chunk 还没到时先看什么"。两者同时在场时骨架在最上层
 * （`z-index: 2`），因为过渡动画跑的是已经加载好的页面。
 */
const loading = computed(() => routePending.value > 0)

/**
 * 过渡壳（票 #76）。
 *
 * 决策由 `composables/useRouteTransition.ts` 的导航守卫在**换页前**写好，
 * 这里只把它读出来交给 `<Transition>`：
 *
 *   - `name`：过渡名（`page-forward` / `page-back` / `page-lateral` / `page-none`），
 *     决定 CSS class，也就决定了这次换页是位移、是淡入，还是**根本没有动画**。
 *   - `--route-shift`：位移量（12px），由决策器给出，CSS 只读不写 ——
 *     两侧各写一个 12 的那种漂移（界面动 24px / 界面动 0）在这里被结构性地排除。
 *
 * ⚠ `page-none`（放映室整页豁免 / reduced-motion 判死 / 首屏 / 同路由）
 * **刻意不定义任何过渡样式**，因此那几种换页是**瞬时**的。
 */
const shell = useRouteTransition()

/** 位移量写进 CSS 变量：CSS 是唯一读它的地方。 */
const stageStyle = computed(() => ({
  '--route-shift': `${shell.value.shiftPx}px`,
}))
</script>

<template>
  <div class="app" :class="{ chromeless }">
    <AppHeader v-if="variant" :variant="variant" />
    <!--
      路由出口 = 过渡舞台（票 #76 与 #79 合并）。

      **焦点只在这里面找主标题**（utils/pageFocus.ts），免得从旧页里捞到一个已经卸载的标题。

      `<Transition>` **不用 mode**：前进/后退要求「新页归零」与「旧页淡出」
      **同时**发生（票 #45 的决议），串行会把一次换页拉成 480ms 并在中间留一帧空白。
      代价是新旧两页短暂同时在场，因此旧页必须脱离文档流 ——
      这件事由 `styles/transition.css` 的 `*-leave-active` 规则负责。

      舞台本身是 `position: relative`：离场的旧页绝对定位在它里面，
      于是「旧页从顶部淡出」对齐的是**内容区顶部**（顶栏下方），不是视口顶部。
      顶栏常驻、换页不动（这正是整屏横移被否掉的理由）。
    -->
    <div id="route-outlet" class="route-stage" :style="stageStyle" data-testid="route-stage">
      <router-view v-slot="{ Component }">
        <Transition :name="shell.name">
          <component :is="Component" />
        </Transition>
      </router-view>
      <!-- chunk 在路上：骨架顶着（"点了没反应"的反面）。盖在舞台之上，等的是还没到的页。 -->
      <RouteSkeleton v-if="loading" class="pending" />
    </div>
    <!-- 提示图层挂在路由之外：换页不重建，正在显示的提示不被卸载 -->
    <TooltipLayer />
    <!--
      轻提示（票 #67）：同样挂在路由之外。用户点了一条"有人关注了你"，
      正在跳去粉丝 tab —— 提示不该在跳转的一瞬间被卸载掉。
    -->
    <ToastLayer />
    <!--
      换页播报（票 #79）：**全站唯一**的 aria-live 区域。
      为什么放在外壳里而不是每个页面各写一个：多个 live region 会各念各的，
      一句页面名被念三遍比不念更糟。
      为什么 sr-only：它是给读屏的，不是给人看的。
    -->
    <div class="sr-only" role="status" aria-live="polite" aria-atomic="true">{{ announcement }}</div>
  </div>
</template>

<style scoped>
.app {
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  flex-direction: column;
}

/* 全屏页面（落地页、登录/注册壳）自己占满视口，不额外撑开 */
.app.chromeless {
  min-height: 100vh;
}

/*
 * 路由出口 = 过渡舞台（票 #76 + 票 #79）。
 *
 * 撑满剩余高度，让内容页的 min-height:100vh 生效。
 *
 * `position: relative` 是**必需**的：离场的旧页是 `position: absolute`，
 * 它的包含块必须是一个已定位的祖先，否则它会贴到**视口**顶部（盖住常驻顶栏），
 * 而不是内容区顶部。
 */
.route-stage {
  position: relative;
  flex: 1;
  display: flex;
  flex-direction: column;
}

/* 骨架盖在旧页之上：等待期间画面不空白 */
.pending {
  position: absolute;
  inset: 0;
  z-index: 2;
  background: var(--bg);
}
</style>
