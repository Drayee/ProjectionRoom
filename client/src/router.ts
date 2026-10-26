import { createRouter, createWebHistory } from 'vue-router'
import HomePage from './pages/HomePage.vue'
import RoomPage from './pages/RoomPage.vue'
import LoginPage from './pages/LoginPage.vue'
import RegisterPage from './pages/RegisterPage.vue'
import HelpPage from './pages/HelpPage.vue'
import PublicRoomsPage from './pages/PublicRoomsPage.vue'
import UserPage from './pages/UserPage.vue'
import ResetPasswordPage from './pages/ResetPasswordPage.vue'
import VerifyEmailPage from './pages/VerifyEmailPage.vue'
import ComingSoonPage from './pages/ComingSoonPage.vue'
import FriendsPage from './pages/FriendsPage.vue'
import MessagesPage from './pages/MessagesPage.vue'
import PostsPage from './pages/PostsPage.vue'
import PostDetailPage from './pages/PostDetailPage.vue'
import PostEditorPage from './pages/PostEditorPage.vue'
import AdminUsersPage from './pages/admin/AdminUsersPage.vue'
import AdminRoomsPage from './pages/admin/AdminRoomsPage.vue'
import AdminOpsPage from './pages/admin/AdminOpsPage.vue'
import OfficialPage from './pages/OfficialPage.vue'

/**
 * 路由表（月夜故障重建 · 票 #18 骨架）。
 *
 * **刻意不加全局守卫**（铁律 I3：授权 100% 在服务端）：
 *   1. 守卫只能控"看到什么"，挡不住任何请求；
 *   2. 观众按房间码进房**本来就不需要登录**；
 *   3. 需要登录的是**具体动作**（建房），由动作入口就地拦截：
 *      `useAuthStore().ensureLoggedIn()`，跳转带 `?redirect=<当前路径>`。
 *
 * 18 条 path/name 与旧表逐字一致（票 #12 契约平移：`/room/:roomId`、
 * `/login`（负向判据）、`/reset`、`/verify`（邮件链接写死）不可改）。
 * 做/占位的分配来自票 #16：
 *   - 真做：/ /room /login /register /reset /verify /help /rooms /user /posts×3 /friends /messages
 *   - 占位（ComingSoonPage，props 驱动）：/forum /about（/official 已由 #34 落地）
 *   - 管理端三屏（#23/#24/#25）：/admin /admin/rooms /admin/ops
 *
 * **`meta.header` 决定全局顶栏形态**（App.vue 常驻外壳，顶栏不属于任何页面）：
 *   app=分区图标导航 / page=内容页 / bare=只有品牌名 / none=无顶栏。
 *   落地页与登录注册壳取 none：它们全屏自足，顶栏会抢首屏焦点。
 */
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    // 落地页：全屏 hero，刻意无顶栏（首屏焦点是月喵标题 + 开始按钮）
    { path: '/', name: 'home', component: HomePage, meta: { header: 'none' } },
    { path: '/room/:roomId', name: 'room', component: RoomPage, props: true, meta: { header: 'app' } },
    { path: '/login', name: 'login', component: LoginPage, meta: { header: 'none' } },
    { path: '/register', name: 'register', component: RegisterPage, meta: { header: 'none' } },
    // 邮件链接落点：token 在 fragment（不进服务端日志），页面挂载时自己读
    // 并立刻抹掉 hash（utils/fragmentToken.ts）。不做成路由参数：那会让 token 进 URL 路径。
    { path: '/reset', name: 'reset', component: ResetPasswordPage, meta: { header: 'bare' } },
    { path: '/verify', name: 'verify', component: VerifyEmailPage, meta: { header: 'bare' } },
    // 帮助页：7 个锚点（#why-login #source #room-code #playback #diagnostics #privacy #faq）
    // 被多处深链引用，scrollBehavior 消费 to.hash —— 锚点 id 必须保留（票 #16）。
    { path: '/help', name: 'help', component: HelpPage },
    // 公开房列表：免登录，与落地页「直接进入放映室」同一条发现路径
    { path: '/rooms', name: 'public-rooms', component: PublicRoomsPage },
    // 管理端三张视图（票 #23 / #24 / #25）。`/admin/rooms` 的筛选与分页放在
    // query（`?search=&public=&offset=`）里，因此那一屏可分享、刷新不丢。
    // `/admin/ops` 的日志/审计分页**刻意不轮询**（轮询只刷指标），否则边看边刷新
    // 会让 offset 分页错位、重复或漏行。
    { path: '/admin', name: 'admin', component: AdminUsersPage, meta: { header: 'page' } },
    { path: '/admin/rooms', name: 'admin-rooms', component: AdminRoomsPage, meta: { header: 'page' } },
    { path: '/admin/ops', name: 'admin-ops', component: AdminOpsPage, meta: { header: 'page' } },
    // 作者介绍：内容形态属独立后续页（旧图 Out of scope），本轮占位
    {
      path: '/about',
      name: 'about',
      component: ComingSoonPage,
      props: {
        title: '关于作者',
        icon: 'star',
        summary: '作者介绍与项目缘起，内容待补充。',
        bullets: ['月喵：主播播放本地视频，观众经 WebRTC 同步观看。'],
        depends: ['作者提供介绍内容（独立后续页）'],
      },
    },
    // 用户档案页（#32）：他人 → 公开档案（免登录，只渲染白名单字段）；
    // 本人 → 资料编辑（昵称）。数据全部经 `api/users.ts` 门面。
    { path: '/user/:id', name: 'user', component: UserPage, props: true, meta: { header: 'page' } },

    // 好友（票 #28）：列表 / 待处理请求 / 按用户名找人 / 黑名单，全部走 api/friends.ts 门面。
    // 「发消息」按 utils/dmRoute.ts 的约定跳到 /messages?peer=<id>（票 #30 解析）。
    { path: '/friends', name: 'friends', component: FriendsPage, meta: { header: 'page' } },
    // 官方 / 推荐放映区（#34）：与 /rooms 共用同一个端点（?official=true）与同一张卡片，
    // 差别只有运营位次排序与页级标识。数据经 api/official.ts 门面。
    { path: '/official', name: 'official', component: OfficialPage, meta: { header: 'page' } },
    {
      path: '/forum',
      name: 'forum',
      component: ComingSoonPage,
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
    { path: '/posts', name: 'posts', component: PostsPage, meta: { header: 'page' } },
    { path: '/posts/new', name: 'post-editor', component: PostEditorPage, meta: { header: 'page' } },
    { path: '/posts/:id', name: 'post-detail', component: PostDetailPage, props: true, meta: { header: 'page' } },
    // 消息（票 #30）：会话列表 / 聊天窗 / 已读上报，全部走 api/messages.ts 门面。
    // **会话是 URL 的一部分**：`/messages?peer=<id>`（utils/dmRoute.ts 的跨票契约）。
    // 页面据此从 URL 恢复会话，因此刷新、新标签页、`/friends` 的「发消息」、
    // 浏览器后退键都自然成立 —— 页面里没有"只改内存的选中态"。
    {
      path: '/messages',
      name: 'messages',
      component: MessagesPage,
      meta: { header: 'page' },
    },
  ],
  /**
   * 滚动行为：`/help#source` 这类锚点深链必须真的滚到那一节。
   * 只做滚动，不做任何跳转判断，与"不加守卫"的约定不冲突。
   */
  scrollBehavior(to, _from, savedPosition) {
    if (savedPosition) return savedPosition
    if (to.hash) {
      return { el: to.hash, top: 16, behavior: 'smooth' }
    }
    return { top: 0 }
  },
})
