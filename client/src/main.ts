import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { installDebugHook } from './debug'
import { tipDirective } from './directives/tip'
import { useAuthStore } from './stores/auth'
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

// 自动化验收钩子（window.__pr）。两个构建期常量条件：生产构建下静态判死，
// Rollup 摇掉整段与 debug.ts，产物里连 window.__pr 字符串都不存在（F-5）。
if (import.meta.env.DEV || __PR_DEBUG_HOOKS__) {
  installDebugHook()
}
