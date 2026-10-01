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
installDebugHook()
