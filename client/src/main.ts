import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { installDebugHook } from './debug'
import { tipDirective } from './directives/tip'
import { useAuthStore } from './stores/auth'
import { installRouteTransitionGuard } from './composables/useRouteTransition'
import './styles/tokens.css'
import './styles/base.css'
import './styles/transition.css'

const app = createApp(App)
app.use(createPinia())

/**
 * 换页守卫必须装在 `app.use(router)` **之前**（票 #76）。
 *
 * vue-router 的首次导航是由 `install()` 内部 `push(currentLocation)` 触发的，
 * 而全局 `beforeEach` 是在**导航发生的那一刻**才被收集的 ——
 * 守卫装晚了，首屏那次导航就没有决策（首屏本来也不需要过渡，
 * 但那样会让"守卫根本没生效"与"首屏不触发"两种情况在真机上看不出区别）。
 */
installRouteTransitionGuard(router)
app.use(router)
// 全局提示指令（v-tip）：必须在 mount 之前注册；图层本体在 App.vue（全局唯一一份）。
app.directive('tip', tipDirective)
app.mount('#app')

// 账号会话启动引导（必须在 pinia 激活后）：三态恢复，失败一律静默。
void useAuthStore().bootstrap()

// 自动化验收钩子（window.__pr）。两个构建期常量条件：生产构建下静态判死，
// Rollup 摇掉整段与 debug.ts，产物里连 window.__pr 字符串都不存在（F-5）。
if (import.meta.env.DEV || __PR_DEBUG_HOOKS__) {
  installDebugHook()
}
