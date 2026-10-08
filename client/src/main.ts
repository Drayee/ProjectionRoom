import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { installDebugHook } from './debug'
import './styles.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')

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
