import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { installDebugHook } from './debug'
import { tipDirective } from './directives/tip'
import { useAuthStore } from './stores/auth'
import './styles.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
// 全局提示指令（`v-tip`）：必须在 mount 之前注册，否则首屏渲染时指令解析不到。
// 图层本体挂在 App.vue 里（全局唯一一份），指令只负责写状态。
app.directive('tip', tipDirective)
app.mount('#app')

// 账号会话的启动引导（必须在 app.use(createPinia()) 之后：useAuthStore() 需要已激活的 pinia）。
//
// 它做三件事，全部**不阻塞挂载**（挂载已经在上面完成了）：
//   - sessionStorage 里有 access token → 同步算已登录，后台校一次 /me 刷新档案；
//   - 只有 localStorage 的档案缓存 → 用 HttpOnly Cookie 走一次 /api/auth/refresh 免密恢复；
//   - 什么都没有 → 一个请求都不发（后端没上线时也一样安静）。
// 失败一律静默：用户可能只是打开了首页，绝不能因为"会话过期"就把人拽去登录页。
// 因此这里**不需要** catch：bootstrap() 内部已经把异常全部收口（连 reject 都不会有）。
void useAuthStore().bootstrap()

// 自动化验收钩子（window.__pr）。必须放在 app.use(createPinia()) 之后：
// useRoomStore() 需要一个已激活的 pinia 实例。
//
// **生产构建里这段必须整段消失**（F-5）：钩子会把整个 Pinia store（房间密码、成员表、
// reportMetrics / setUploadThrottle）挂到 window 上，等于给页面留了一个后门。
//
// 两个条件都是**构建期常量**：
//   - `import.meta.env.DEV`：Vite dev server 下恒为 true，验收脚本（test/script/verify-*.mjs）
//     跑的就是它，所以钩子名与行为在 DEV 下保持完全兼容；
//   - `__PR_DEBUG_HOOKS__`：由 vite.config.ts 的 define 注入（`VITE_DEBUG_HOOKS=1` 时为 true）。
// 生产构建两者都是 false → 条件被静态判死 → Rollup 摇掉整段与 debug.ts，
// 产物里连 `window.__pr` 这个字符串都不会存在。
if (import.meta.env.DEV || __PR_DEBUG_HOOKS__) {
  installDebugHook()
}
