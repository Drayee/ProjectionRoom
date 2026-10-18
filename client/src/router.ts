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
import PostsPage from './pages/PostsPage.vue'
import PostDetailPage from './pages/PostDetailPage.vue'
import PostEditorPage from './pages/PostEditorPage.vue'
import AdminUsersPage from './pages/admin/AdminUsersPage.vue'
import AdminRoomsPage from './pages/admin/AdminRoomsPage.vue'
import AdminOpsPage from './pages/admin/AdminOpsPage.vue'

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
 *   - 真做：/ /room /login /register /reset /verify /help /rooms /user /posts×3 /friends
 *   - 占位（ComingSoonPage，props 驱动）：/official /forum /messages /about
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
    // 用户详情页：本轮只支持本人（公开档案接口尚未实现）
    { path: '/user/:id', name: 'user', component: UserPage, props: true, meta: { header: 'page' } },

    // 好友（票 #28）：列表 / 待处理请求 / 按用户名找人 / 黑名单，全部走 api/friends.ts 门面。
    // 「发消息」按 utils/dmRoute.ts 的约定跳到 /messages?peer=<id>（票 #30 解析）。
    { path: '/friends', name: 'friends', component: FriendsPage, meta: { header: 'page' } },
    {
      path: '/official',
      name: 'official',
      component: ComingSoonPage,
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
    {
      path: '/messages',
      name: 'messages',
      component: ComingSoonPage,
      props: {
        title: '消息',
        icon: 'msg',
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
