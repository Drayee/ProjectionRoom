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
import { announcement } from './utils/pageAnnouncer'
import { routePending } from './utils/routeLoadState'
import { useAuthStore } from './stores/auth'

type HeaderVariant = 'app' | 'page' | 'bare'

const route = useRoute()
const auth = useAuthStore()

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
 */
const loading = computed(() => routePending.value > 0)
</script>

<template>
  <div class="app" :class="{ chromeless }">
    <AppHeader v-if="variant" :variant="variant" />
    <!--
      路由出口容器：焦点只在这里面找主标题（utils/pageFocus.ts），
      免得从旧页里捞到一个已经卸载的标题。
    -->
    <div id="route-outlet" class="outlet">
      <router-view v-slot="{ Component }">
        <component :is="Component" />
      </router-view>
      <!-- chunk 在路上：骨架顶着（"点了没反应"的反面）。 -->
      <RouteSkeleton v-if="loading" class="pending" />
    </div>
    <!-- 提示图层挂在路由之外：换页不重建，正在显示的提示不被卸载 -->
    <TooltipLayer />
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

/* 路由出口：撑满剩余高度，让内容页的 min-height:100vh 生效 */
.outlet {
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
