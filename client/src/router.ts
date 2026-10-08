import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import RoomView from './views/RoomView.vue'
import LoginView from './views/LoginView.vue'
import RegisterView from './views/RegisterView.vue'

/**
 * 路由表。
 *
 * **刻意不加全局守卫**（规格 §11：授权 100% 在服务端）：
 *   1. 守卫只能控"看到什么"，挡不住任何请求；把授权判断写在客户端等于把安全边界搬到前端；
 *   2. 观众按房间码进房**本来就不需要登录**，一个"未登录一律跳登录"的守卫会顺手把这扇门关上；
 *   3. 需要登录的是**具体动作**（建房），由动作入口就地拦截：`useAuthStore().ensureLoggedIn()`
 *      跳转时带上 `?redirect=<当前路径>`，登录成功后回到发起处。
 */
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: HomeView },
    { path: '/room/:roomId', name: 'room', component: RoomView, props: true },
    { path: '/login', name: 'login', component: LoginView },
    { path: '/register', name: 'register', component: RegisterView },
  ],
})
