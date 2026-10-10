import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import RoomView from './views/RoomView.vue'
import LoginView from './views/LoginView.vue'
import RegisterView from './views/RegisterView.vue'
import HelpView from './views/HelpView.vue'
import PublicRoomsView from './views/PublicRoomsView.vue'
import AdminView from './views/AdminView.vue'
import AboutView from './views/AboutView.vue'
import UserView from './views/UserView.vue'
import ComingSoonView from './views/ComingSoonView.vue'

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
 *（未登录：落地页 → 点「开始」展开登录窗口；已登录：标题栏 + 核心功能页）—— 仍然只需要这一条路由。
 *
 * 本批新增的路由分两类：
 *   - **真的有内容**：`/about`（作者介绍，最小占位并标注"待补充"）、`/user/:id`（自己的账号资料，
 *     本轮最小页）；
 *   - **占位（建设中）**：房间室的好友/官方、番剧论坛、帖子、消息 —— 六条路由先注册好，
 *     入口在首页标题栏的分区里，页面本体是 `ComingSoonView`（写清"建设中 + 预期形态 + 依赖"，
 *     不发任何请求）。后端与数据模型随后另行实现。
 *
 * 注：房间室的「公开」**不新增路由** —— 直接复用既有的 `/rooms`（`PublicRoomsView`，真实数据、
 * 免登录），因为"发现公开房间"本来就与"按房间码进房"并列，而不是登录后的特权。
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
    // 公开房间列表（二期 T5）：**免登录**，与落地页的「直接进入放映室」是同一条路径 ——
    // 因此这里同样不设守卫，未登录的人照样能看到并点进任何一个公开房。
    { path: '/rooms', name: 'public-rooms', component: PublicRoomsView },
    // 管理端（二期 T7）：**不设守卫**。页面的"能不能进"由服务端响应决定（403 → 无权限 +
    // 返回首页），本地守卫既拦不住请求、又会把"权限不足"说成"没登录"。
    { path: '/admin', name: 'admin', component: AdminView },
    // 作者介绍页：内容待补充，入口在落地页右上角与顶栏（同一个路由）。
    { path: '/about', name: 'about', component: AboutView },
    // 用户详情页：本轮只支持本人（公开档案接口尚未实现，页面里直说）。
    { path: '/user/:id', name: 'user', component: UserView, props: true },

    // ---------- 分区占位页（建设中）----------
    // 内容写在路由的 props 里：一份渲染逻辑，六份说明，避免复制粘贴后各自漂移。
    {
      path: '/friends',
      name: 'friends',
      component: ComingSoonView,
      props: {
        title: '房间室 · 好友',
        icon: 'users-group',
        summary: '和固定的人一起看：好友关系、在线状态，以及"好友正在看的房间"。',
        bullets: [
          '好友列表：双向确认的关系（请求 / 接受 / 删除），不再只靠房间码找人。',
          '好友动态：谁在线、谁正在看片、房间公开性允许时的一键跟看。',
          '邀请进房：把当前房间推给好友（仍遵守房间密码与公开性）。',
        ],
        depends: ['friendships 表的读写接口', '用户级实时通道（/ws/user，用于在线状态与邀请）'],
      },
    },
    {
      path: '/official',
      name: 'official',
      component: ComingSoonView,
      props: {
        title: '房间室 · 官方',
        icon: 'shield-done',
        summary: '官方与推荐的放映：运营位、活动房、以及被标记为官方的时间表。',
        bullets: [
          '官方房间列表：由运营设置，与用户自建的公开房分开呈现。',
          '活动时间表：即将开始的放映与提醒。',
          '审核与下架：官方列表要能一键撤下（复用管理端已有的强关/下架能力）。',
        ],
        depends: ['房间元数据里的"官方/推荐"标记与运营权限', '（可选）按时间表排序的查询接口'],
      },
    },
    {
      path: '/forum',
      name: 'forum',
      component: ComingSoonView,
      props: {
        title: '番剧论坛',
        icon: 'chat',
        summary: '按番剧聚合的讨论区：一部番一个板块，板块下面是主题串。',
        bullets: [
          '番剧条目 → 板块 → 主题串 → 回复的四层结构。',
          '与"添加番剧"共用同一份目录数据（现在那个入口也还是"能力待接入"）。',
          '排序与检索：最新回复优先，支持按番剧名/关键词找板块。',
        ],
        depends: ['番剧目录（anime 条目）的数据模型', '帖子/主题表（与 /posts 共用）'],
      },
    },
    {
      path: '/posts',
      name: 'posts',
      component: ComingSoonView,
      props: {
        title: '帖子界面',
        icon: 'tips',
        summary: '帖子列表与详情：发帖、回复、引用，和论坛共用同一套帖子模型。',
        bullets: [
          '发帖与回复（对应快速入口里的「发送帖子」）。',
          '列表页：最新/热门两个维度，带分页。',
          '内容一律**文本插值**渲染：不引入 HTML 注入（v-html / innerHTML 在本项目是禁区）。',
        ],
        depends: ['posts / replies 表的读写接口', '发帖限速（与既有按 IP 令牌桶同一套机制）'],
      },
    },
    {
      path: '/messages',
      name: 'messages',
      component: ComingSoonView,
      props: {
        title: '消息',
        icon: 'share',
        summary: '私聊与系统通知：会话列表、未读计数、进房邀请。',
        bullets: [
          '会话列表 + 未读计数（账号档案里的 unread 字段早就预留好了，现在恒为 0）。',
          '系统通知：房间被关闭、被下架、账号状态变更这类事件。',
          '实时投递：与好友在线状态共用一条用户级通道。',
        ],
        depends: ['messages 表的读写接口', '用户级实时通道（/ws/user）'],
      },
    },
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
