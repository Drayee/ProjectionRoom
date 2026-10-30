import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { installDebugHook } from './debug'
import { tipDirective } from './directives/tip'
import { useAuthStore } from './stores/auth'
import { useRoutePageMeta } from './composables/useRoutePageMeta'
import { installRouteLoadWatch } from './utils/routeLoadState'
import './styles/tokens.css'
import './styles/base.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
// 全局提示指令（v-tip）：必须在 mount 之前注册；图层本体在 App.vue（全局唯一一份）。
app.directive('tip', tipDirective)
app.mount('#app')

// 账号会话启动引导（必须在 pinia 激活后）：三态恢复，失败一律静默。
void useAuthStore().bootstrap()

// 换页的副作用（票 #79）：文档标题单点写、焦点进主标题、aria-live 播报一次。
// 必须在 mount 之后调用：它要用 useRoute()（依赖 router 已装）与组件生命周期。
useRoutePageMeta(router)

// 懒加载途中的"在途"计数（票 #79）：换页期间用骨架顶着。
installRouteLoadWatch(router)

// 自动化验收钩子（window.__pr）。两个构建期常量条件：生产构建下静态判死，
// Rollup 摇掉整段与 debug.ts，产物里连 window.__pr 字符串都不存在（F-5）。
if (import.meta.env.DEV || __PR_DEBUG_HOOKS__) {
  installDebugHook()
}
