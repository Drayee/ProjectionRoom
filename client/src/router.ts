import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import RoomView from './views/RoomView.vue'
import LoginView from './views/LoginView.vue'
import RegisterView from './views/RegisterView.vue'
import HelpView from './views/HelpView.vue'
import PublicRoomsView from './views/PublicRoomsView.vue'
import AdminView from './views/AdminView.vue'

/**
 * 路由表。
 *
 * **刻意不加全局守卫**（规格 §11：授权 100% 在服务端）：
 *   1. 守卫只能控"看到什么"，挡不住任何请求；把授权判断写在客户端等于把安全边界搬到前端；
 *   2. 观众按房间码进房**本来就不需要登录**，一个"未登录一律跳登录"的守卫会顺手把这扇门关上；
 *   3. 需要登录的是**具体动作**（建房），由动作入口就地拦截：`useAuthStore().ensureLoggedIn()`
 *      跳转时带上 `?redirect=<当前路径>`，登录成功后回到发起处。
 *
 * 未登录访问 `/` 不再"跳登录页"，而是由 `HomeView` 自己按登录态换渲染内容
 *（未登录：登录/注册 + 直接进入房间；已登录：核心功能页）—— 仍然只需要这一条路由。
 */
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: HomeView },
    { path: '/room/:roomId', name: 'room', component: RoomView, props: true },
    { path: '/login', name: 'login', component: LoginView },
    { path: '/register', name: 'register', component: RegisterView },
    // 说明页：所有长篇解释集中在这里，核心页面只留一句话 + 图标入口。
    { path: '/help', name: 'help', component: HelpView },
    // 公开房间列表（二期 T5）：**免登录**，与落地页的「直接进入房间」是同一条路径 ——
    // 因此这里同样不设守卫，未登录的人照样能看到并点进任何一个公开房。
    { path: '/rooms', name: 'public-rooms', component: PublicRoomsView },
    // 管理端（二期 T7）：**不设守卫**。页面的"能不能进"由服务端响应决定（403 → 无权限 +
    // 返回首页），本地守卫既拦不住请求、又会把"权限不足"说成"没登录"。
    { path: '/admin', name: 'admin', component: AdminView },
  ],
  /**
   * 滚动行为：`/help#source` 这类深链（首页的「片源准备」、建房/进房卡片里的"为什么？"）
   * 必须真的滚到那一节，否则用户点了图标会以为"没反应"。
   * 这里只做滚动，不做任何跳转判断，因此与"不加守卫"的约定不冲突。
   */
  scrollBehavior(to, _from, savedPosition) {
    if (savedPosition) return savedPosition
    if (to.hash) {
      return { el: to.hash, top: 16, behavior: 'smooth' }
    }
    return { top: 0 }
  },
})
