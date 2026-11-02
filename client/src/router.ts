import { createRouter, createWebHistory } from 'vue-router'
// 落地页**静态导入**（唯一例外）：它是全屏 hero，必须首屏即到。
// 懒加载它会让首屏多一次 chunk 往返 —— 首屏慢半拍是本项目最不能接受的那种慢。
// 其余 19 条路由全部走 utils/routePages.ts 的动态 import。
import HomePage from './pages/HomePage.vue'
import { PAGE_MODULES } from './utils/routePages'

/**
 * vue-router 的 `RouteMeta` 类型增强（票 #76）。
 *
 * `RouteMeta` 默认是 `Record<PropertyKey, unknown>`：写 `meta: { depth: 2 }`
 * 不会报错，但**读出来也是 unknown** —— 于是 `from.meta.depth` 拿不到类型。
 * 这里把两个字段声明出来，编译器就能守住它们：
 *
 *   depth   路由层级（0 落地 / 1 列表 / 2 详情），决定换页过渡方向
 *   exempt  整页豁免（只有 `/room/:roomId`），放映室进出**零过渡**
 *
 * 声明必须与 `utils/routeTransition.ts` 的 `TransitionRouteMeta` 一致 ——
 * 那边是决策器的入参契约。
 */
declare module 'vue-router' {
  interface RouteMeta {
    /** 路由层级：0 落地 / 1 列表 / 2 详情。缺省 1（见 routeDepth）。 */
    depth?: number
    /** 整页豁免：换页零过渡（放映室：WebRTC 与播放器怕 DOM 抖动）。 */
    exempt?: boolean
  }
}

/**
 * 路由表（月夜故障重建 · 票 #18 骨架；票 #79 改成全懒加载 + 单点标题）。
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
 *   - 占位（ComingSoonPage，props 驱动）：/about（/official 与 /forum 已由 #34/#36 落地）
 *   - 管理端三屏（#23/#24/#25）：/admin /admin/rooms /admin/ops
 *
 * **`<router-view>` 决定全局顶栏形态**（App.vue 常驻外壳，顶栏不属于任何页面）：
 *   app=分区图标导航 / page=内容页 / bare=只有品牌名 / none=无顶栏。
 *   落地页与登录注册壳取 none：它们全屏自足，顶栏会抢首屏焦点。
 *
 * ## 懒加载（票 #79）
 *
 * 20 条路由的组件**全部**是 `() => import(...)`：首包只剩外壳 + 落地页，
 * 管理端三屏（只有管理员用得到）与低频页（番剧板块、发帖页…）不再压在首屏上。
 *
 * 加载函数统一从 `utils/routePages.ts` 取，**不在这里内联写 import**：
 * 预热（`utils/routePrefetch.ts`）要用同一份函数，写两遍必然漂移。
 *
 * ## 文档标题（票 #79）
 *
 * `meta.title` 是页面名的**唯一**声明处，`utils/routeMeta.ts` 负责把它拼成
 * 「页面名 · 月喵」并写进 `document.title` —— **单点写**（GLOSSARY「Route transition」）：
 * 页面里各自写 `document.title` 会让多标签页下出现三种口径，而用户无从判断
 * 哪条才是当前页。
 *
 * ## 换页过渡方向（票 #76）
 *
 * **`meta.depth` 决定换页过渡的方向**（票 #45 的决议）：
 *   0 = 落地页    1 = 列表/分区    2 = 详情
 *   方向由**两个 depth 的比较**得出，不用浏览器历史推断 ——
 *   `pushState` 与 `back()` 在 URL 上不可区分，靠历史猜方向必然猜错。
 *   `meta.exempt = true` 的路由（只有 `/room/:roomId`）**整页豁免**：
 *   房里有 WebRTC 与播放器，DOM 抖动会打断媒体管线。
 *   判据与决策器在 `utils/routeTransition.ts`（纯函数，可单测）。
 *
 * ⚠ `depth` 与 `title` 是**两条正交的轴**：前者给过渡决策器读，后者给
 * `document.title` 读。新增路由时两个都要给 —— 漏 `depth` 会退化成缺省 1
 * （同级纯淡入，不报错但叙事方向丢失），漏 `title` 则浏览器标签页停在上一页。
 */
export const router = createRouter({
  history: createWebHistory(),
  routes: [
    // 落地页：全屏 hero，刻意无顶栏（首屏焦点是月喵标题 + 开始按钮）
    {
      path: '/',
      name: 'home',
      component: HomePage,
      meta: { header: 'none', title: '首页', depth: 0 },
    },
    {
      path: '/room/:roomId',
      name: 'room',
      component: PAGE_MODULES.room,
      props: true,
      meta: {
        header: 'app',
        // 房间标题是**房间码**：它只在 params 里，不在静态表里，
        // 所以这里用函数形态（utils/routeMeta.ts 的 RouteTitle）。
        title: (route) => String(route.params.roomId ?? '').toUpperCase(),
        // 放映室整页豁免：房里有 WebRTC 与播放器，DOM 抖动会打断媒体管线。
        depth: 2,
        exempt: true,
      },
    },
    {
      path: '/login',
      name: 'login',
      component: PAGE_MODULES.login,
      meta: { header: 'none', title: '登录', depth: 1 },
    },
    {
      path: '/register',
      name: 'register',
      component: PAGE_MODULES.register,
      meta: { header: 'none', title: '注册', depth: 1 },
    },
    // 邮件链接落点：token 在 fragment（不进服务端日志），页面挂载时自己读
    // 并立刻抹掉 hash（utils/fragmentToken.ts）。不做成路由参数：那会让 token 进 URL 路径。
    //
    // ⚠ 这里曾有 `/reset`（"点邮件里的重置链接改密"）。它随 `/api/auth/forgot` +
    // `/api/auth/reset` 一起下线：改密码现在走**邮箱验证码**，入口在 `/login?tab=forgot`
    // 的两段式表单里，不需要一个独立的落点页。
    // 旧邮件里的 `/reset#token=…` 因此会落到 404 —— 这是有意的取舍（见 docs/ACCOUNTS.md）。
    {
      path: '/verify',
      name: 'verify',
      component: PAGE_MODULES.verify,
      meta: { header: 'bare', title: '验证邮箱', depth: 1 },
    },
    // 帮助页：7 个锚点（#why-login #source #room-code #playback #diagnostics #privacy #faq）
    // 被多处深链引用，scrollBehavior 消费 to.hash —— 锚点 id 必须保留（票 #16）。
    { path: '/help', name: 'help', component: PAGE_MODULES.help, meta: { title: '帮助', depth: 1 } },
    // 公开房列表：免登录，与落地页「直接进入放映室」同一条发现路径
    {
      path: '/rooms',
      name: 'public-rooms',
      component: PAGE_MODULES['public-rooms'],
      meta: { title: '公开房间', depth: 1 },
    },
    // 管理端三张视图（票 #23 / #24 / #25）。`/admin/rooms` 的筛选与分页放在
    // query（`?search=&public=&offset=`）里，因此那一屏可分享、刷新不丢。
    // `/admin/ops` 的日志/审计分页**刻意不轮询**（轮询只刷指标），否则边看边刷新
    // 会让 offset 分页错位、重复或漏行。
    {
      path: '/admin',
      name: 'admin',
      component: PAGE_MODULES.admin,
      meta: { header: 'page', title: '管理端 · 用户', depth: 1 },
    },
    {
      path: '/admin/rooms',
      name: 'admin-rooms',
      component: PAGE_MODULES['admin-rooms'],
      meta: { header: 'page', title: '管理端 · 房间', depth: 1 },
    },
    {
      path: '/admin/ops',
      name: 'admin-ops',
      component: PAGE_MODULES['admin-ops'],
      meta: { header: 'page', title: '管理端 · 运维', depth: 1 },
    },
    // 作者介绍：内容形态属独立后续页（旧图 Out of scope），本轮占位
    {
      path: '/about',
      name: 'about',
      component: PAGE_MODULES.about,
      props: {
        title: '关于作者',
        icon: 'star',
        summary: '作者介绍与项目缘起，内容待补充。',
        bullets: ['月喵：主播播放本地视频，观众经 WebRTC 同步观看。'],
        depends: ['作者提供介绍内容（独立后续页）'],
      },
      meta: { title: '关于作者', depth: 1 },
    },
    // 用户档案页（#32）：他人 → 公开档案（免登录，只渲染白名单字段）；
    // 本人 → 资料编辑（昵称）。数据全部经 `api/users.ts` 门面。
    {
      path: '/user/:id',
      name: 'user',
      component: PAGE_MODULES.user,
      props: true,
      meta: { header: 'page', title: '用户档案', depth: 2 },
    },
    /**
     * 我的管理台（票 #69）。
     *
     * 与 `/user/:id` **分成两条路由**（决议 #47）：`/user/:id` 对本人也变成
     * 纯公开视角，所有管理动作（改昵称、删帖、我的房、关系名单、设置入口）
     * 收在这里。旧实现在同一条路由里按 `isSelf` 切两套视图 —— 那套分支已拆除：
     * 它的症状是"同一个地址，登录前后是两个页面"，分享与后退都说不清。
     *
     * `?tab=` 是跨票契约，键名唯一源在 `utils/meRoute.ts`（与 `dmRoute` /
     * `friendsRoute` / `postsRoute` 同族）。`?tab=settings` 的内容由票 #71 填，
     * 本票只把槽位与键名钉下来。
     *
     * `depth: 1`：它是**分区级**页面（与 `/friends`、`/posts` 同级），
     * 从任何详情页进来都是"退一层"的方向，换页位移不会反。
     */
    {
      path: '/me',
      name: 'me',
      component: PAGE_MODULES.me,
      meta: { header: 'page', title: '我的管理台', depth: 1 },
    },

    // 好友（票 #28）：列表 / 待处理请求 / 按用户名找人 / 黑名单，全部走 api/friends.ts 门面。
    // 「发消息」按 utils/dmRoute.ts 的约定跳到 /messages?peer=<id>（票 #30 解析）。
    {
      path: '/friends',
      name: 'friends',
      component: PAGE_MODULES.friends,
      meta: { header: 'page', title: '好友', depth: 1 },
    },
    // 官方 / 推荐放映区（#34）：与 /rooms 共用同一个端点（?official=true）与同一张卡片，
    // 差别只有运营位次排序与页级标识。数据经 api/official.ts 门面。
    {
      path: '/official',
      name: 'official',
      component: PAGE_MODULES.official,
      meta: { header: 'page', title: '官方放映区', depth: 1 },
    },
    // 番剧论坛（#36）：目录（`?q=&offset=` 进 URL）→ 板块（`/forum/:animeId`）→ 主题串
    // （复用 `/posts/:id` 的详情与评论，不造第二套）。数据经 api/forum.ts 门面。
    {
      path: '/forum',
      name: 'forum',
      component: PAGE_MODULES.forum,
      meta: { header: 'page', title: '番剧论坛', depth: 1 },
    },
    {
      path: '/forum/:animeId',
      name: 'anime-board',
      component: PAGE_MODULES['anime-board'],
      props: true,
      meta: { header: 'page', title: '番剧板块', depth: 2 },
    },
    {
      path: '/posts',
      name: 'posts',
      component: PAGE_MODULES.posts,
      meta: { header: 'page', title: '帖子', depth: 1 },
    },
    {
      path: '/posts/new',
      name: 'post-editor',
      component: PAGE_MODULES['post-editor'],
      meta: { header: 'page', title: '发帖', depth: 2 },
    },
    {
      path: '/posts/:id',
      name: 'post-detail',
      component: PAGE_MODULES['post-detail'],
      props: true,
      meta: { header: 'page', title: '帖子详情', depth: 2 },
    },
    // 消息（票 #30）：会话列表 / 聊天窗 / 已读上报，全部走 api/messages.ts 门面。
    // **会话是 URL 的一部分**：`/messages?peer=<id>`（utils/dmRoute.ts 的跨票契约）。
    // 页面据此从 URL 恢复会话，因此刷新、新标签页、`/friends` 的「发消息」、
    // 浏览器后退键都自然成立 —— 页面里没有"只改内存的选中态"。
    {
      path: '/messages',
      name: 'messages',
      component: PAGE_MODULES.messages,
      meta: { header: 'page', title: '消息', depth: 1 },
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
